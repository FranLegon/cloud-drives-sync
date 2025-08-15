package task

import (
	"cloud-drives-sync/internal/api"
	"cloud-drives-sync/internal/config"
	"cloud-drives-sync/internal/database"
	"cloud-drives-sync/internal/logger"
	"cloud-drives-sync/internal/model"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/manifoldco/promptui"
)

// TaskRunner orchestrates all complex, multi-step operations.
type TaskRunner struct {
	Config    *config.Config
	DB        *database.DB
	Clients   map[string]api.CloudClient // map[email]client
	IsSafeRun bool
}

// GetMetadata fetches metadata for all configured main accounts and their shared content.
func (tr *TaskRunner) GetMetadata() error {
	var wg sync.WaitGroup
	errChan := make(chan error, len(tr.Config.Users))

	for _, user := range tr.Config.Users {
		if !user.IsMain {
			continue
		}
		wg.Add(1)
		go func(u model.User) {
			defer wg.Done()
			client, ok := tr.Clients[u.Email]
			if !ok {
				errChan <- fmt.Errorf("client not found for %s", u.Email)
				return
			}
			logger.TaggedInfo(u.Email, "Starting metadata scan...")
			syncFolderID, err := client.PreFlightCheck()
			if err != nil {
				errChan <- fmt.Errorf("[%s] pre-flight check failed: %w", u.Email, err)
				return
			}
			if syncFolderID == "" {
				logger.Warn(u.Email, nil, "'synched-cloud-drives' folder not found. Run 'init' for this account to create it.")
				return
			}
			// Scan folders first to build the path map
			if err := client.ListFolders(syncFolderID, "", tr.folderProcessor); err != nil {
				errChan <- fmt.Errorf("[%s] folder scan failed: %w", u.Email, err)
			}
			// Then scan files
			if err := client.ListFiles(syncFolderID, "", tr.fileProcessor); err != nil {
				errChan <- fmt.Errorf("[%s] file scan failed: %w", u.Email, err)
			}
			logger.TaggedInfo(u.Email, "Metadata scan complete.")
		}(user)
	}
	wg.Wait()
	close(errChan)

	// Return the first error encountered from any goroutine.
	for err := range errChan {
		if err != nil {
			return err
		}
	}
	return nil
}

// folderProcessor is the callback for ListFolders.
func (tr *TaskRunner) folderProcessor(folder model.Folder) error {
	return tr.DB.UpsertFolder(folder)
}

// fileProcessor is the callback for ListFiles.
func (tr *TaskRunner) fileProcessor(file model.File) error {
	if file.HashAlgorithm == "SHA256" && file.FileHash == "" {
		client := tr.Clients[file.OwnerEmail]
		logger.TaggedInfo(file.OwnerEmail, "Calculating SHA256 for '%s'...", file.FileName)

		body, _, err := client.DownloadFile(file.FileID)
		if err != nil {
			logger.Warn(file.OwnerEmail, err, "could not download file for hashing")
			return nil // Skip this file but continue the scan.
		}
		defer body.Close()

		h := sha256.New()
		if _, err := io.Copy(h, body); err != nil {
			logger.Warn(file.OwnerEmail, err, "could not hash file content")
			return nil
		}
		file.FileHash = fmt.Sprintf("%x", h.Sum(nil))
	}
	return tr.DB.UpsertFile(file)
}

// CheckForDuplicates finds and reports files with identical content hashes.
func (tr *TaskRunner) CheckForDuplicates() (map[string][]model.File, error) {
	logger.Info("Updating local metadata before checking for duplicates...")
	if err := tr.GetMetadata(); err != nil {
		return nil, fmt.Errorf("metadata update failed: %w", err)
	}
	logger.Info("Querying database for duplicates...")
	duplicates, err := tr.DB.FindDuplicates()
	if err != nil {
		return nil, fmt.Errorf("database query failed: %w", err)
	}

	if len(duplicates) == 0 {
		logger.Info("No duplicate files found.")
		return nil, nil
	}

	logger.Info("Found duplicate files:")
	for hashKey, files := range duplicates {
		provider := files[0].Provider
		hash := strings.Split(hashKey, ":")[1]
		fmt.Printf("\n--- Duplicates in %s for Hash: %s ---\n", provider, hash)
		for _, file := range files {
			fmt.Printf("  - Path: %s\n    Owner: %s, Created: %s, Size: %.2f KB\n", file.Path, file.OwnerEmail, file.CreatedOn.Format(time.RFC822), float64(file.FileSize)/1024)
		}
	}
	return duplicates, nil
}

// RemoveDuplicatesUnsafe automatically deletes all but the oldest copy of a file.
func (tr *TaskRunner) RemoveDuplicatesUnsafe() error {
	duplicates, err := tr.CheckForDuplicates()
	if err != nil || duplicates == nil {
		return err // Error or no duplicates found
	}

	for _, files := range duplicates {
		toKeep := files[0]
		toDelete := files[1:]

		logger.Info("Keeping oldest file: '%s' (Created: %s)", toKeep.Path, toKeep.CreatedOn.Format(time.RFC822))

		for _, fileToDelete := range toDelete {
			if tr.IsSafeRun {
				logger.DryRun(fileToDelete.OwnerEmail, "DELETE file '%s' (ID: %s)", fileToDelete.Path, fileToDelete.FileID)
				continue
			}
			client := tr.Clients[fileToDelete.OwnerEmail]
			logger.TaggedInfo(fileToDelete.OwnerEmail, "Deleting duplicate '%s'", fileToDelete.Path)
			if err := client.DeleteFile(fileToDelete.FileID); err != nil {
				logger.Warn(fileToDelete.OwnerEmail, err, "failed to delete file")
			} else {
				tr.DB.DeleteFile(fileToDelete.FileID, fileToDelete.Provider)
			}
		}
	}
	return nil
}

// CheckTokens verifies all stored refresh tokens are valid by making a simple API call.
func (tr *TaskRunner) CheckTokens() {
	var wg sync.WaitGroup
	for email, client := range tr.Clients {
		wg.Add(1)
		go func(e string, c api.CloudClient) {
			defer wg.Done()
			if _, err := c.GetAbout(); err != nil {
				logger.Warn(e, err, "token check FAILED. Re-authentication may be required.")
			} else {
				logger.TaggedInfo(e, "Token is valid.")
			}
		}(email, client)
	}
	wg.Wait()
}

// SyncProviders mirrors content between the main Google and Microsoft accounts.
func (tr *TaskRunner) SyncProviders() error {
	logger.Info("Starting provider sync. This may take a while...")
	logger.Info("Step 1: Ensuring local metadata is up-to-date.")
	if err := tr.GetMetadata(); err != nil {
		return fmt.Errorf("metadata update failed: %w", err)
	}

	var googleMain, msMain model.User
	var googleClient, msClient api.CloudClient
	for _, u := range tr.Config.Users {
		if u.IsMain {
			switch u.Provider {
			case "Google":
				googleMain, googleClient = u, tr.Clients[u.Email]
			case "Microsoft":
				msMain, msClient = u, tr.Clients[u.Email]
			}
		}
	}
	if googleClient == nil || msClient == nil {
		return fmt.Errorf("a main account for both Google and Microsoft must be configured to run sync")
	}

	logger.Info("Step 2: Loading file and folder lists from database.")
	gFiles, _ := tr.DB.GetFilesByProvider("Google")
	mFiles, _ := tr.DB.GetFilesByProvider("Microsoft")
	gFolders, _ := tr.DB.GetFoldersByProvider("Google")
	mFolders, _ := tr.DB.GetFoldersByProvider("Microsoft")

	gFileMap := make(map[string]model.File)
	for _, f := range gFiles {
		gFileMap[f.NormalizedPath] = f
	}
	mFileMap := make(map[string]model.File)
	for _, f := range mFiles {
		mFileMap[f.NormalizedPath] = f
	}

	gFolderMap := make(map[string]model.Folder)
	for _, f := range gFolders {
		gFolderMap[f.NormalizedPath] = f
	}
	mFolderMap := make(map[string]model.Folder)
	for _, f := range mFolders {
		mFolderMap[f.NormalizedPath] = f
	}

	// Sync logic: Google -> Microsoft
	logger.Info("Step 3: Syncing items from Google (%s) to Microsoft (%s).", googleMain.Email, msMain.Email)
	msSyncRootID, _ := msClient.PreFlightCheck()
	for path := range gFolderMap {
		if _, exists := mFolderMap[path]; !exists {
			if _, err := tr.ensurePathExists(msClient, path, msSyncRootID, mFolderMap); err != nil {
				logger.Warn("sync", err, "could not create folder structure on Microsoft for %s", path)
			}
		}
	}
	for path, gFile := range gFileMap {
		mFile, exists := mFileMap[path]
		if !exists {
			logger.TaggedInfo("sync", "File '%s' missing on Microsoft, will upload.", gFile.Path)
			tr.transferFile(googleClient, msClient, gFile, gFile.FileName, mFolderMap)
		} else if gFile.FileHash != mFile.FileHash {
			logger.TaggedInfo("sync", "Conflict for '%s'. Renaming and uploading.", gFile.Path)
			ext := filepath.Ext(gFile.FileName)
			base := strings.TrimSuffix(gFile.FileName, ext)
			conflictName := fmt.Sprintf("%s_conflict_%s%s", base, time.Now().Format("2006-01-02"), ext)
			tr.transferFile(googleClient, msClient, gFile, conflictName, mFolderMap)
		}
	}

	// Sync logic: Microsoft -> Google
	logger.Info("Step 4: Syncing items from Microsoft (%s) to Google (%s).", msMain.Email, googleMain.Email)
	gSyncRootID, _ := googleClient.PreFlightCheck()
	for path := range mFolderMap {
		if _, exists := gFolderMap[path]; !exists {
			if _, err := tr.ensurePathExists(googleClient, path, gSyncRootID, gFolderMap); err != nil {
				logger.Warn("sync", err, "could not create folder structure on Google for %s", path)
			}
		}
	}
	for path, mFile := range mFileMap {
		if _, exists := gFileMap[path]; !exists {
			logger.TaggedInfo("sync", "File '%s' missing on Google, will upload.", mFile.Path)
			tr.transferFile(msClient, googleClient, mFile, mFile.FileName, gFolderMap)
		}
	}

	logger.Info("Provider sync complete.")
	return nil
}

// BalanceStorage moves files from accounts over 95% full to backups until they are below 90%.
func (tr *TaskRunner) BalanceStorage() error {
	logger.Info("Checking storage quotas...")
	accountsByProvider := make(map[string][]model.User)
	for _, u := range tr.Config.Users {
		accountsByProvider[u.Provider] = append(accountsByProvider[u.Provider], u)
	}

	for provider, accounts := range accountsByProvider {
		if len(accounts) < 2 {
			continue
		}

		quotas := tr.getQuotasForUsers(accounts)
		for _, fullAccountQuota := range quotas {
			usage := float64(fullAccountQuota.UsedBytes) / float64(fullAccountQuota.TotalBytes)
			if usage > 0.95 {
				logger.TaggedInfo(provider, "Account %s is %.1f%% full. Attempting to balance.", fullAccountQuota.OwnerEmail, usage*100)

				var backups []model.StorageQuota
				for _, q := range quotas {
					isBackup := false
					for _, u := range accounts {
						if u.Email == q.OwnerEmail && !u.IsMain {
							isBackup = true
							break
						}
					}
					if isBackup {
						backups = append(backups, q)
					}
				}
				sort.Slice(backups, func(i, j int) bool { return backups[i].RemainingBytes > backups[j].RemainingBytes })

				if len(backups) == 0 {
					logger.Warn(provider, nil, "Account %s is full but no backup accounts are available.", fullAccountQuota.OwnerEmail)
					continue
				}

				allFiles, _ := tr.DB.GetFilesByProvider(provider)
				var sourceFiles []model.File
				for _, f := range allFiles {
					if f.OwnerEmail == fullAccountQuota.OwnerEmail {
						sourceFiles = append(sourceFiles, f)
					}
				}
				sort.Slice(sourceFiles, func(i, j int) bool { return sourceFiles[i].FileSize > sourceFiles[j].FileSize })

				sourceClient := tr.Clients[fullAccountQuota.OwnerEmail]
				currentUsage := usage

				for _, fileToMove := range sourceFiles {
					if currentUsage < 0.90 {
						break
					}

					targetQuota := backups[0]
					targetClient := tr.Clients[targetQuota.OwnerEmail]
					logger.TaggedInfo(provider, "Moving '%.2f' MB file '%s' from %s to %s", float64(fileToMove.FileSize)/1024/1024, fileToMove.FileName, fullAccountQuota.OwnerEmail, targetQuota.OwnerEmail)

					if tr.IsSafeRun {
						logger.DryRun(provider, "MOVE '%s' from %s to %s", fileToMove.FileName, fullAccountQuota.OwnerEmail, targetQuota.OwnerEmail)
						// Simulate the move for dry run calculations
						currentUsage -= (float64(fileToMove.FileSize) / float64(fullAccountQuota.TotalBytes))
						continue
					}

					moved, err := tr.moveFileBetweenAccounts(sourceClient, targetClient, fileToMove)
					if err != nil {
						logger.Warn(provider, err, "failed to move file")
					} else if moved {
						fileToMove.OwnerEmail = targetQuota.OwnerEmail
						tr.DB.UpsertFile(fileToMove)
						currentUsage -= (float64(fileToMove.FileSize) / float64(fullAccountQuota.TotalBytes))
						backups[0].RemainingBytes -= fileToMove.FileSize
						sort.Slice(backups, func(i, j int) bool { return backups[i].RemainingBytes > backups[j].RemainingBytes })
					}
				}
			}
		}
	}
	return nil
}

// FreeMain transfers all files from a main account to its backup accounts.
func (tr *TaskRunner) FreeMain() error {
	provider, err := selectProvider("Select provider for which to free the main account")
	if err != nil {
		return err
	}

	var mainUser model.User
	var backupUsers []model.User
	for _, u := range tr.Config.Users {
		if u.Provider == provider {
			if u.IsMain {
				mainUser = u
			} else {
				backupUsers = append(backupUsers, u)
			}
		}
	}

	if mainUser.Email == "" {
		return fmt.Errorf("no main account found for %s", provider)
	}
	if len(backupUsers) == 0 {
		return fmt.Errorf("no backup accounts found for %s to move files to", provider)
	}

	mainClient := tr.Clients[mainUser.Email]
	mainFiles, _ := tr.DB.GetFilesByProvider(provider)
	var filesToMove []model.File
	var totalSizeToMove int64
	for _, f := range mainFiles {
		if f.OwnerEmail == mainUser.Email {
			filesToMove = append(filesToMove, f)
			totalSizeToMove += f.FileSize
		}
	}

	if len(filesToMove) == 0 {
		logger.Info("Main account %s has no files in the sync folder. Nothing to do.", mainUser.Email)
		return nil
	}

	backupQuotas := tr.getQuotasForUsers(backupUsers)
	var totalBackupSpace int64
	for _, q := range backupQuotas {
		totalBackupSpace += q.RemainingBytes
	}

	if totalSizeToMove > totalBackupSpace {
		return fmt.Errorf("not enough space in backup accounts. Required: %.2f GB, Available: %.2f GB", float64(totalSizeToMove)/1024/1024/1024, float64(totalBackupSpace)/1024/1024/1024)
	}
	logger.Info("Moving %d files (%.2f GB) from %s to backup accounts.", len(filesToMove), float64(totalSizeToMove)/1024/1024/1024, mainUser.Email)

	for _, file := range filesToMove {
		// Sort backups by remaining space before each move to pick the best target.
		sort.Slice(backupQuotas, func(i, j int) bool { return backupQuotas[i].RemainingBytes > backupQuotas[j].RemainingBytes })
		targetAccount := backupQuotas[0]
		targetClient := tr.Clients[targetAccount.OwnerEmail]

		logger.TaggedInfo(provider, "Moving '%s' to %s", file.FileName, targetAccount.OwnerEmail)
		if tr.IsSafeRun {
			logger.DryRun(provider, "MOVE '%s' from %s to %s", file.FileName, mainUser.Email, targetAccount.OwnerEmail)
			// Simulate space change for dry run
			backupQuotas[0].RemainingBytes -= file.FileSize
			continue
		}

		moved, err := tr.moveFileBetweenAccounts(mainClient, targetClient, file)
		if err != nil {
			logger.Warn(provider, err, "failed to move file, stopping operation.")
			return err
		}
		if moved {
			// Update local state to reflect the move for the next iteration.
			file.OwnerEmail = targetAccount.OwnerEmail
			tr.DB.UpsertFile(file)
			backupQuotas[0].RemainingBytes -= file.FileSize
		}
	}
	logger.Info("Successfully freed main account %s.", mainUser.Email)
	return nil
}

// ShareWithMain verifies and re-applies share permissions for all backup accounts.
func (tr *TaskRunner) ShareWithMain() error {
	logger.Info("Verifying and repairing share permissions...")
	accountsByProvider := make(map[string][]model.User)
	for _, u := range tr.Config.Users {
		accountsByProvider[u.Provider] = append(accountsByProvider[u.Provider], u)
	}

	for provider, accounts := range accountsByProvider {
		var mainUser model.User
		var mainClient api.CloudClient
		var backupUsers []model.User
		foundMain := false
		for _, u := range accounts {
			if u.IsMain {
				mainUser, mainClient, foundMain = u, tr.Clients[u.Email], true
			} else {
				backupUsers = append(backupUsers, u)
			}
		}

		if !foundMain || len(backupUsers) == 0 {
			continue
		}

		logger.TaggedInfo(provider, "Checking main account %s...", mainUser.Email)
		syncFolderID, err := mainClient.PreFlightCheck()
		if err != nil || syncFolderID == "" {
			logger.Warn(provider, err, "could not find or access sync folder for main account, skipping permission repair.")
			continue
		}

		for _, backup := range backupUsers {
			logger.TaggedInfo(provider, "Ensuring %s has editor access to the sync folder.", backup.Email)
			if tr.IsSafeRun {
				logger.DryRun(provider, "SHARE sync folder with %s", backup.Email)
				continue
			}
			// This is an idempotent operation; re-sharing is safe if it already exists.
			if _, err := mainClient.Share(syncFolderID, backup.Email); err != nil {
				logger.Warn(provider, err, "failed to apply share permission for %s", backup.Email)
			}
		}
	}
	logger.Info("Permission repair check complete.")
	return nil
}

// --- Helper functions for TaskRunner ---

func (tr *TaskRunner) getQuotasForUsers(users []model.User) []model.StorageQuota {
	var wg sync.WaitGroup
	quotaChan := make(chan model.StorageQuota, len(users))
	for _, u := range users {
		wg.Add(1)
		go func(user model.User) {
			defer wg.Done()
			client, ok := tr.Clients[user.Email]
			if !ok {
				return
			}
			if q, err := client.GetAbout(); err == nil {
				quotaChan <- *q
			}
		}(u)
	}
	wg.Wait()
	close(quotaChan)
	var quotas []model.StorageQuota
	for q := range quotaChan {
		quotas = append(quotas, q)
	}
	return quotas
}

func (tr *TaskRunner) ensurePathExists(client api.CloudClient, path string, rootID string, folderMap map[string]model.Folder) (string, error) {
	if path == "/" || path == "" {
		return rootID, nil
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	currentParentID := rootID
	currentPath := ""

	for _, part := range parts {
		currentPath = currentPath + "/" + part
		normalized := strings.ToLower(currentPath)

		if folder, exists := folderMap[normalized]; exists {
			currentParentID = folder.FolderID
			continue
		}

		logger.TaggedInfo("helper", "Creating missing folder '%s' on %s", part, client.GetProviderName())
		if tr.IsSafeRun {
			logger.DryRun(client.GetProviderName(), "CREATE FOLDER '%s' in parent %s", part, currentParentID)
			// For dry run, we need a placeholder ID to continue and update the map.
			currentParentID = "dry-run-folder-id-" + part
			folderMap[normalized] = model.Folder{FolderID: currentParentID, Path: currentPath, NormalizedPath: normalized}
			continue
		}

		newFolder, err := client.CreateFolder(currentParentID, part)
		if err != nil {
			return "", fmt.Errorf("failed to create folder %s: %w", part, err)
		}
		newFolder.Path = currentPath
		newFolder.NormalizedPath = normalized
		folderMap[normalized] = *newFolder
		currentParentID = newFolder.FolderID
	}
	return currentParentID, nil
}

func (tr *TaskRunner) transferFile(sourceClient, targetClient api.CloudClient, file model.File, newName string, targetFolderMap map[string]model.Folder) {
	if tr.IsSafeRun {
		logger.DryRun(sourceClient.GetProviderName(), "TRANSFER file '%s' to %s", file.Path, targetClient.GetProviderName())
		return
	}

	parentPath := filepath.Dir(strings.ReplaceAll(file.Path, "\\", "/"))
	if parentPath == "." || parentPath == "\\" {
		parentPath = "/"
	}
	targetRootID, _ := targetClient.PreFlightCheck()
	targetParentFolderID, err := tr.ensurePathExists(targetClient, parentPath, targetRootID, targetFolderMap)
	if err != nil {
		logger.Warn("transfer", err, "cannot create destination folder for %s", file.FileName)
		return
	}

	logger.TaggedInfo("transfer", "Downloading '%s' from %s", file.FileName, sourceClient.GetProviderName())
	body, size, err := sourceClient.DownloadFile(file.FileID)
	if err != nil {
		logger.Warn("transfer", err, "download failed for %s", file.FileName)
		return
	}
	defer body.Close()

	logger.TaggedInfo("transfer", "Uploading '%s' to %s", newName, targetClient.GetProviderName())
	uploadedFile, err := targetClient.UploadFile(targetParentFolderID, newName, body, size)
	if err != nil {
		logger.Warn("transfer", err, "upload failed for %s", newName)
		return
	}

	uploadedFile.Path = parentPath + "/" + newName
	uploadedFile.NormalizedPath = strings.ToLower(uploadedFile.Path)
	tr.DB.UpsertFile(*uploadedFile)
	logger.TaggedInfo("transfer", "Successfully transferred '%s'", file.FileName)
}

func (tr *TaskRunner) moveFileBetweenAccounts(sourceClient, targetClient api.CloudClient, file model.File) (bool, error) {
	targetEmail, err := targetClient.GetUserEmail()
	if err != nil {
		return false, fmt.Errorf("could not get target user email: %w", err)
	}

	transferred, _ := sourceClient.TransferOwnership(file.FileID, targetEmail)
	if transferred {
		logger.TaggedInfo("move", "Successfully transferred ownership of '%s'.", file.FileName)
		return true, nil
	}
	logger.TaggedInfo("move", "Native ownership transfer failed or not supported. Falling back to download/upload/delete.")

	targetFolderMap := make(map[string]model.Folder)
	folders, _ := tr.DB.GetFoldersByProvider(targetClient.GetProviderName())
	for _, f := range folders {
		targetFolderMap[f.NormalizedPath] = f
	}

	tr.transferFile(sourceClient, targetClient, file, file.FileName, targetFolderMap)

	logger.TaggedInfo("move", "Deleting original file '%s' from %s.", file.FileName, sourceClient.GetProviderName())
	if err := sourceClient.DeleteFile(file.FileID); err != nil {
		// This is a critical error. The file is now duplicated across accounts.
		logger.Error(err, "CRITICAL: Failed to delete original file after copy. '%s' is now duplicated across accounts.", file.FileName)
		return false, err
	}
	tr.DB.DeleteFile(file.FileID, file.Provider)

	return true, nil
}

// selectProvider is a helper for cmd packages to use.
func selectProvider(label string) (string, error) {
	prompt := promptui.Select{
		Label: label,
		Items: []string{"Google", "Microsoft"},
	}
	_, result, err := prompt.Run()
	if err != nil {
		logger.Info("Operation cancelled.")
		os.Exit(0)
	}
	return result, nil
}
