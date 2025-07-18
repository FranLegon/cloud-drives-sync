package task

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/api"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/auth"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/config"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/database"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/google"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/logger"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/microsoft"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/model"
	"golang.org/x/oauth2"
)

// TaskRunner orchestrates all complex operations by coordinating the interactions
// between configuration, the database, and the cloud provider clients.
type TaskRunner struct {
	MasterPassword string
	Config         *config.AppConfig
	DB             database.DB
	Clients        map[string]api.CloudClient // Keyed by user email
	IsSafeRun      bool
}

// NewTaskRunner creates and initializes a fully operational TaskRunner. It loads the config,
// connects to the database, and initializes an API client for every user.
func NewTaskRunner(masterPassword string, safeRun bool) (*TaskRunner, error) {
	cfg, err := config.LoadConfig(masterPassword)
	if err != nil {
		return nil, err
	}

	db, err := database.NewDB(masterPassword)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}

	clients := make(map[string]api.CloudClient)
	for _, user := range cfg.Users {
		ctx := context.Background()
		ts, err := auth.NewTokenSource(ctx, &user, cfg)
		if err != nil {
			// Don't fail completely, just log a warning. The check-tokens command can be used to fix this.
			logger.TaggedError(user.Email, "Failed to create token source, client for this user will be unavailable: %v", err)
			continue
		}

		var client api.CloudClient
		if user.Provider == "Google" {
			httpClient := oauth2.NewClient(ctx, ts)
			client, err = google.NewClient(httpClient)
		} else if user.Provider == "Microsoft" {
			client, err = microsoft.NewClient(ts)
		} else {
			return nil, fmt.Errorf("unknown provider in config: %s", user.Provider)
		}

		if err != nil {
			return nil, fmt.Errorf("failed to create api client for %s: %w", user.Email, err)
		}
		clients[user.Email] = client
	}

	return &TaskRunner{
		MasterPassword: masterPassword,
		Config:         cfg,
		DB:             db,
		Clients:        clients,
		IsSafeRun:      safeRun,
	}, nil
}

// runPreFlightChecks executes the pre-flight check for each main account.
// It returns a map of provider name to its sync folder ID.
func (tr *TaskRunner) runPreFlightChecks(ctx context.Context) (map[string]string, error) {
	syncFolderIDs := make(map[string]string)
	for _, user := range tr.Config.Users {
		if user.IsMain {
			client, ok := tr.Clients[user.Email]
			if !ok {
				return nil, fmt.Errorf("client for main account '%s' is unavailable. please check credentials using 'check-tokens'", user.Email)
			}
			folderID, err := client.PreflightCheck(ctx)
			if err != nil {
				return nil, err
			}
			syncFolderIDs[user.Provider] = folderID
		}
	}
	return syncFolderIDs, nil
}

// GetMetadata scans all configured cloud accounts, processes file and folder metadata,
// handles hashing (with fallback), and updates the local database.
func (tr *TaskRunner) GetMetadata(ctx context.Context) error {
	logger.Info("Starting metadata retrieval...")
	syncFolderIDs, err := tr.runPreFlightChecks(ctx)
	if err != nil {
		return fmt.Errorf("pre-flight checks failed: %w", err)
	}

	var wg sync.WaitGroup
	errChan := make(chan error, len(tr.Config.Users))

	for _, user := range tr.Config.Users {
		client, ok := tr.Clients[user.Email]
		if !ok {
			logger.TaggedError(user.Email, "Skipping metadata retrieval; client is unavailable.")
			continue
		}

		wg.Add(1)
		go func(user config.User, client api.CloudClient) {
			defer wg.Done()
			var rootID string
			var ok bool
			if user.IsMain {
				rootID, ok = syncFolderIDs[user.Provider]
			} else {
				// For backup accounts, the sync folder is the one shared by the main account.
				// We assume it's also named SyncFolderName and is at the top-level of what's visible.
				// A more robust solution would be to find the folder shared by the main account's email.
				// For now, we search for it by name.
				id, err := client.PreflightCheck(ctx)
				if err != nil {
					errChan <- fmt.Errorf("failed pre-flight for backup account %s: %w", user.Email, err)
					return
				}
				rootID, ok = id, true
			}

			if !ok {
				errChan <- fmt.Errorf("could not determine sync folder ID for user %s", user.Email)
				return
			}

			logger.TaggedInfo(user.Email, "Listing all files and folders...")
			cloudFiles, cloudFolders, err := client.ListAllFilesAndFolders(ctx, rootID)
			if err != nil {
				errChan <- fmt.Errorf("failed to list items for %s: %w", user.Email, err)
				return
			}
			logger.TaggedInfo(user.Email, "Found %d files and %d folders in the cloud.", len(cloudFiles), len(cloudFolders))

			// Process all metadata and update DB
			if err := tr.processAndStoreMetadata(ctx, user, client, rootID, cloudFiles, cloudFolders); err != nil {
				errChan <- err
			}
		}(user, client)
	}

	wg.Wait()
	close(errChan)

	// Check for any errors from goroutines
	for e := range errChan {
		if e != nil {
			return e // Return first error encountered
		}
	}

	logger.Info("Metadata retrieval and database update complete.")
	return nil
}

// processAndStoreMetadata is a helper to process items from a single account and update the database.
func (tr *TaskRunner) processAndStoreMetadata(ctx context.Context, user config.User, client api.CloudClient, rootID string, cloudFiles []api.FileInfo, cloudFolders []api.FolderInfo) error {
	// Build a map of folderID -> parentID to reconstruct paths
	parentMap := make(map[string]string)
	for _, f := range cloudFolders {
		if len(f.ParentFolderIDs) > 0 {
			parentMap[f.ID] = f.ParentFolderIDs[0]
		}
	}

	folderPathMap := make(map[string]string) // folderID -> full path
	folderPathMap[rootID] = "/"

	// Function to get the full path of a folder
	var getPath func(string) (string, error)
	getPath = func(folderID string) (string, error) {
		if path, ok := folderPathMap[folderID]; ok {
			return path, nil
		}
		parentID, ok := parentMap[folderID]
		if !ok {
			return "", fmt.Errorf("folder %s has no parent in map", folderID)
		}
		parentPath, err := getPath(parentID)
		if err != nil {
			return "", err
		}
		var folderName string
		for _, f := range cloudFolders {
			if f.ID == folderID {
				folderName = f.Name
				break
			}
		}
		path := filepath.Join(parentPath, folderName)
		folderPathMap[folderID] = path
		return path, nil
	}

	// Update folders in DB
	for _, cf := range cloudFolders {
		path, err := getPath(cf.ID)
		if err != nil {
			logger.TaggedError(user.Email, "Could not construct path for folder %s (%s): %v", cf.Name, cf.ID, err)
			continue
		}
		folder := model.Folder{
			FolderID:       cf.ID,
			Provider:       user.Provider,
			OwnerEmail:     user.Email,
			FolderName:     cf.Name,
			ParentFolderID: parentMap[cf.ID],
			Path:           path,
			NormalizedPath: strings.ToLower(filepath.ToSlash(path)),
			LastSynced:     time.Now().UTC(),
		}
		if err := tr.DB.UpsertFolder(&folder); err != nil {
			return fmt.Errorf("db error upserting folder %s: %w", folder.FolderName, err)
		}
	}

	// Update files in DB
	for _, cfile := range cloudFiles {
		// Hashing fallback logic
		if err := tr.ensureFileHash(ctx, user.Email, client, &cfile); err != nil {
			logger.TaggedError(user.Email, "Skipping file '%s' due to hashing error: %v", cfile.Name, err)
			continue
		}

		if cfile.Hash == "" || cfile.HashAlgorithm == "" {
			logger.TaggedError(user.Email, "Skipping file '%s' as it has no usable hash.", cfile.Name)
			continue
		}

		parentID := ""
		if len(cfile.ParentFolderIDs) > 0 {
			parentID = cfile.ParentFolderIDs[0]
		}

		file := model.File{
			FileID:         cfile.ID,
			Provider:       user.Provider,
			OwnerEmail:     user.Email, // Assign ownership to the scanning account
			FileHash:       cfile.Hash,
			HashAlgorithm:  cfile.HashAlgorithm,
			FileName:       cfile.Name,
			FileSize:       cfile.Size,
			ParentFolderID: parentID,
			CreatedOn:      cfile.CreatedTime,
			LastModified:   cfile.ModifiedTime,
			LastSynced:     time.Now().UTC(),
		}
		if err := tr.DB.UpsertFile(&file); err != nil {
			return fmt.Errorf("db error upserting file %s: %w", file.FileName, err)
		}
	}
	return nil
}

// ensureFileHash handles the hashing fallback for proprietary file types.
func (tr *TaskRunner) ensureFileHash(ctx context.Context, userEmail string, client api.CloudClient, file *api.FileInfo) error {
	if file.IsProprietary {
		logger.TaggedInfo(userEmail, "Downloading proprietary file '%s' for local hashing...", file.Name)

		// For Google Docs/Sheets, export as PDF.
		exportMime := "application/pdf"

		rc, err := client.ExportFile(ctx, file.ID, exportMime)
		if err != nil {
			return fmt.Errorf("failed to export '%s': %w", file.Name, err)
		}
		defer rc.Close()

		h := sha256.New()
		if _, err := io.Copy(h, rc); err != nil {
			return fmt.Errorf("failed to hash exported file '%s': %w", file.Name, err)
		}
		file.Hash = fmt.Sprintf("%x", h.Sum(nil))
		file.HashAlgorithm = "SHA256"
		logger.TaggedInfo(userEmail, "Successfully hashed '%s' with SHA256.", file.Name)
	}
	return nil
}

// CheckForDuplicates finds and reports files with identical content hashes within each provider.
func (tr *TaskRunner) CheckForDuplicates(ctx context.Context) error {
	logger.Info("Updating metadata before checking for duplicates...")
	if err := tr.GetMetadata(ctx); err != nil {
		return err
	}

	logger.Info("Querying database for duplicate files...")
	for _, provider := range []string{"Google", "Microsoft"} {
		logger.Info("--- Checking Provider: %s ---", provider)
		hashes, err := tr.DB.GetDuplicateHashes(provider)
		if err != nil {
			return fmt.Errorf("failed to get duplicate hashes for %s: %w", provider, err)
		}

		if len(hashes) == 0 {
			logger.Info("No duplicate files found for this provider.")
			continue
		}

		for hash, count := range hashes {
			// We need to query by hash and algo. Since we don't know the algo here, we try both.
			files, err := tr.DB.GetFilesByHash(provider, hash, "MD5")
			if err != nil {
				return err
			}
			qxorFiles, err := tr.DB.GetFilesByHash(provider, hash, "quickXorHash")
			if err != nil {
				return err
			}
			shaFiles, err := tr.DB.GetFilesByHash(provider, hash, "SHA256")
			if err != nil {
				return err
			}
			files = append(files, qxorFiles...)
			files = append(files, shaFiles...)

			if len(files) > 1 {
				fmt.Printf("\nFound %d duplicates for hash: %s\n", len(files), hash)
				for _, file := range files {
					fmt.Printf("  - FileName: %s (ID: %s, Owner: %s, Size: %d)\n", file.FileName, file.FileID, file.OwnerEmail, file.FileSize)
				}
			}
		}
	}
	return nil
}

// SyncProviders ensures file content is identical between the main Google and Microsoft accounts.
func (tr *TaskRunner) SyncProviders(ctx context.Context) error {
	logger.Info("Starting provider synchronization...")
	mainAccounts := make(map[string]config.User)
	for _, u := range tr.Config.Users {
		if u.IsMain {
			mainAccounts[u.Provider] = u
		}
	}
	googleUser, googleOK := mainAccounts["Google"]
	msUser, msOK := mainAccounts["Microsoft"]
	if !googleOK || !msOK {
		return errors.New("a main account for both Google and Microsoft must be configured to sync providers")
	}

	logger.Info("Step 1: Updating local metadata cache...")
	if err := tr.GetMetadata(ctx); err != nil {
		return err
	}

	logger.Info("Step 2: Comparing file sets between providers...")
	googleFiles, err := tr.DB.GetAllFilesByProvider("Google")
	if err != nil {
		return err
	}
	msFiles, err := tr.DB.GetAllFilesByProvider("Microsoft")
	if err != nil {
		return err
	}

	googleFileMap := createFileMap(googleFiles)
	msFileMap := createFileMap(msFiles)

	// Sync from Google to Microsoft
	logger.Info("--- Syncing from Google to Microsoft ---")
	if err := tr.syncDirection(ctx, "Google", "Microsoft", googleUser, msUser, googleFileMap, msFileMap); err != nil {
		return err
	}

	// Sync from Microsoft to Google
	logger.Info("--- Syncing from Microsoft to Google ---")
	if err := tr.syncDirection(ctx, "Microsoft", "Google", msUser, googleUser, msFileMap, googleFileMap); err != nil {
		return err
	}

	logger.Info("Provider synchronization complete.")
	return nil
}

// BalanceStorage moves files from accounts over 95% capacity to other accounts
// of the same provider with more free space.
func (tr *TaskRunner) BalanceStorage(ctx context.Context) error {
	logger.Info("Starting storage balancing...")
	if _, err := tr.runPreFlightChecks(ctx); err != nil {
		return err
	}

	accountsByProvider := make(map[string][]config.User)
	for _, u := range tr.Config.Users {
		accountsByProvider[u.Provider] = append(accountsByProvider[u.Provider], u)
	}

	for provider, accounts := range accountsByProvider {
		logger.Info("--- Balancing Provider: %s ---", provider)
		if len(accounts) < 2 {
			logger.Info("Skipping, not enough accounts to balance.")
			continue
		}

		quotas := make(map[string]*api.QuotaInfo)
		for _, acc := range accounts {
			client, ok := tr.Clients[acc.Email]
			if !ok {
				logger.TaggedError(acc.Email, "Skipping quota check, client unavailable.")
				continue
			}
			quota, err := client.GetStorageQuota(ctx)
			if err != nil {
				return fmt.Errorf("failed to get quota for %s: %w", acc.Email, err)
			}
			quotas[acc.Email] = quota
		}

		// Main balancing loop
		for {
			var sourceUser config.User
			var sourceQuota *api.QuotaInfo

			// Find an account over 95% full
			for _, acc := range accounts {
				q := quotas[acc.Email]
				if q != nil && q.TotalBytes > 0 && (float64(q.UsedBytes)/float64(q.TotalBytes)) > 0.95 {
					sourceUser = acc
					sourceQuota = q
					break
				}
			}

			if sourceUser.Email == "" {
				logger.Info("No accounts are over the 95%% threshold. Balancing for %s is complete.", provider)
				break // Exit loop for this provider
			}

			logger.TaggedInfo(sourceUser.Email, "Account is at %.2f%% capacity, attempting to free space.", (float64(sourceQuota.UsedBytes)/float64(sourceQuota.TotalBytes))*100)

			// Find best destination account (most free space, under 90%)
			var destUser config.User
			var maxFreeSpace int64 = -1
			for _, acc := range accounts {
				q := quotas[acc.Email]
				if acc.Email != sourceUser.Email && q != nil && q.TotalBytes > 0 && (float64(q.UsedBytes)/float64(q.TotalBytes)) < 0.90 {
					if q.FreeBytes > maxFreeSpace {
						maxFreeSpace = q.FreeBytes
						destUser = acc
					}
				}
			}

			if destUser.Email == "" {
				logger.Error("Account %s is full, but no backup accounts with sufficient space are available!", sourceUser.Email)
				break // No destination available
			}

			// Get all files owned by the source user and move the largest one
			allFiles, err := tr.DB.GetAllFilesByProvider(provider)
			if err != nil {
				return err
			}

			var userFiles []model.File
			for _, f := range allFiles {
				if f.OwnerEmail == sourceUser.Email {
					userFiles = append(userFiles, f)
				}
			}
			sort.Slice(userFiles, func(i, j int) bool { return userFiles[i].FileSize > userFiles[j].FileSize })

			if len(userFiles) == 0 {
				logger.TaggedInfo(sourceUser.Email, "Account is full but owns no files in the sync directory.")
				break
			}

			fileToMove := userFiles[0]

			// Perform the move
			err = tr.moveFileBetweenAccounts(ctx, fileToMove, sourceUser, destUser)
			if err != nil {
				logger.TaggedError(sourceUser.Email, "Failed to move file '%s': %v. Stopping balance for this user.", fileToMove.FileName, err)
				break
			}

			// Update local quota objects to reflect the move
			quotas[sourceUser.Email].UsedBytes -= fileToMove.FileSize
			quotas[sourceUser.Email].FreeBytes += fileToMove.FileSize
			quotas[destUser.Email].UsedBytes += fileToMove.FileSize
			quotas[destUser.Email].FreeBytes -= fileToMove.FileSize

			// Check if source is now below 90%
			if (float64(quotas[sourceUser.Email].UsedBytes) / float64(quotas[sourceUser.Email].TotalBytes)) < 0.90 {
				logger.TaggedInfo(sourceUser.Email, "Account is now below 90%%. Balance for this user is complete.")
			}
		}
	}

	logger.Info("Storage balancing complete.")
	return nil
}

// Close gracefully closes the database connection.
func (tr *TaskRunner) Close() error {
	if tr.DB != nil {
		logger.Info("Closing database connection.")
		return tr.DB.Close()
	}
	return nil
}

// --- Helper Functions ---

// syncDirection handles one-way synchronization between a source and destination provider.
func (tr *TaskRunner) syncDirection(ctx context.Context, sourceProvider, destProvider string, sourceUser, destUser config.User, sourceMap, destMap map[string]model.File) error {
	sourceClient := tr.Clients[sourceUser.Email]
	destClient := tr.Clients[destUser.Email]

	for path, sourceFile := range sourceMap {
		destFile, exists := destMap[path]

		// Case 1: File is missing at destination
		if !exists {
			logger.Info("'%s' is missing on %s, uploading...", sourceFile.FileName, destProvider)
			if tr.IsSafeRun {
				logger.TaggedInfo("DRY RUN", "Would upload '%s' to %s", sourceFile.FileName, destProvider)
				continue
			}
			err := tr.transferFile(ctx, sourceFile, sourceClient, destClient)
			if err != nil {
				logger.Error("Failed to transfer '%s': %v", sourceFile.FileName, err)
				continue // Continue with next file
			}
		} else { // Case 2: File exists, check hash
			if sourceFile.FileHash != destFile.FileHash {
				logger.Info("Conflict detected for '%s'. Renaming and uploading.", sourceFile.FileName)
				if tr.IsSafeRun {
					logger.TaggedInfo("DRY RUN", "Would upload '%s' as a conflict-renamed file to %s", sourceFile.FileName, destProvider)
					continue
				}
				// Rename with suffix and transfer
				sourceFile.FileName = addConflictSuffix(sourceFile.FileName)
				err := tr.transferFile(ctx, sourceFile, sourceClient, destClient)
				if err != nil {
					logger.Error("Failed to transfer conflict file '%s': %v", sourceFile.FileName, err)
					continue
				}
			}
		}
	}
	return nil
}

// transferFile orchestrates the download from source and upload to destination.
func (tr *TaskRunner) transferFile(ctx context.Context, file model.File, sourceClient, destClient api.CloudClient) error {
	// 1. Find or create destination folder path
	folderPath := filepath.Dir(file.Path)
	destFolder, err := tr.findOrCreatePath(ctx, destClient, folderPath, destClient.GetUserInfo(ctx))
	if err != nil {
		return err
	}

	// 2. Download from source
	rc, err := sourceClient.DownloadFile(ctx, file.FileID)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer rc.Close()

	// 3. Upload to destination
	_, err = destClient.UploadFile(ctx, destFolder.ID, file.FileName, file.FileSize, rc)
	if err != nil {
		return fmt.Errorf("upload failed: %w", err)
	}
	return nil
}

// moveFileBetweenAccounts handles the logic for moving a file, trying native transfer first.
func (tr *TaskRunner) moveFileBetweenAccounts(ctx context.Context, file model.File, sourceUser, destUser config.User) error {
	sourceClient := tr.Clients[sourceUser.Email]
	destClient := tr.Clients[destUser.Email]

	logger.Info("Moving file '%s' from %s to %s", file.FileName, sourceUser.Email, destUser.Email)
	if tr.IsSafeRun {
		logger.TaggedInfo("DRY RUN", "Would move file '%s' (%s) from %s to %s.", file.FileName, file.FileID, sourceUser.Email, destUser.Email)
		return nil
	}

	// 1. Attempt native ownership transfer
	transferred, err := sourceClient.TransferOwnership(ctx, file.FileID, destUser.Email)
	if err != nil {
		logger.TaggedError(sourceUser.Email, "Ownership transfer API call failed: %v. Falling back to copy.", err)
	}
	if transferred {
		logger.Info("Successfully transferred ownership of '%s'.", file.FileName)
		file.OwnerEmail = destUser.Email
		return tr.DB.UpsertFile(&file)
	}

	// 2. Fallback to Download/Upload/Delete
	logger.Info("Ownership transfer failed or not supported. Falling back to copy/delete method.")
	rc, err := sourceClient.DownloadFile(ctx, file.FileID)
	if err != nil {
		return fmt.Errorf("fallback download failed for %s: %w", file.FileName, err)
	}
	defer rc.Close()

	// For moves, we place it in the destination's root sync folder.
	destRootID, err := destClient.PreflightCheck(ctx)
	if err != nil {
		return err
	}

	newFileInfo, err := destClient.UploadFile(ctx, destRootID, file.FileName, file.FileSize, rc)
	if err != nil {
		return fmt.Errorf("fallback upload failed for %s: %w", file.FileName, err)
	}

	err = sourceClient.DeleteItem(ctx, file.FileID)
	if err != nil {
		// This is bad. We have a copy but failed to delete the original.
		return fmt.Errorf("CRITICAL: Fallback upload succeeded but failed to delete original file %s (%s). Please resolve manually. Error: %w", file.FileName, file.FileID, err)
	}

	// Update the database with the new file info
	err = tr.DB.DeleteFile(file.Provider, file.FileID)
	if err != nil {
		return err
	}

	file.FileID = newFileInfo.ID
	file.OwnerEmail = destUser.Email
	file.ParentFolderID = destRootID
	return tr.DB.UpsertFile(&file)
}

// findOrCreatePath finds a folder by its normalized path, creating it if it doesn't exist.
func (tr *TaskRunner) findOrCreatePath(ctx context.Context, client api.CloudClient, path string, ownerEmail string) (*api.FolderInfo, error) {
	normPath := strings.ToLower(filepath.ToSlash(path))
	if normPath == "/" || normPath == "." {
		rootID, err := client.PreflightCheck(ctx)
		if err != nil {
			return nil, err
		}
		return &api.FolderInfo{ID: rootID}, nil
	}

	folder, err := tr.DB.FindFolderByPath(client.GetUserInfo(ctx), normPath)
	if err != nil {
		return nil, err
	}
	if folder != nil {
		// This needs to be fleshed out to convert model.Folder to api.FolderInfo
		return &api.FolderInfo{ID: folder.FolderID, Name: folder.FolderName}, nil
	}

	// Not in DB, create it on the provider
	parentPath := filepath.Dir(path)
	folderName := filepath.Base(path)

	parentFolder, err := tr.findOrCreatePath(ctx, client, parentPath, ownerEmail)
	if err != nil {
		return nil, err
	}

	logger.Info("Creating folder '%s' inside '%s'", folderName, parentPath)
	if tr.IsSafeRun {
		logger.TaggedInfo("DRY RUN", "Would create folder '%s' in parent %s", folderName, parentFolder.ID)
		return &api.FolderInfo{ID: "dry-run-folder-id"}, nil // return dummy for dry run
	}

	createdFolder, err := client.CreateFolder(ctx, parentFolder.ID, folderName)
	if err != nil {
		return nil, err
	}

	// Add new folder to DB
	newDBFolder := model.Folder{
		FolderID:       createdFolder.ID,
		Provider:       client.GetUserInfo(ctx),
		OwnerEmail:     ownerEmail,
		FolderName:     createdFolder.Name,
		ParentFolderID: parentFolder.ID,
		Path:           path,
		NormalizedPath: normPath,
		LastSynced:     time.Now().UTC(),
	}
	tr.DB.UpsertFolder(&newDBFolder)
	return createdFolder, nil
}

func createFileMap(files []model.File) map[string]model.File {
	m := make(map[string]model.File)
	for _, f := range files {
		m[f.NormalizedPath] = f
	}
	return m
}

func addConflictSuffix(filename string) string {
	ext := filepath.Ext(filename)
	base := strings.TrimSuffix(filename, ext)
	timestamp := time.Now().UTC().Format("2006-01-02")
	return fmt.Sprintf("%s_conflict_%s%s", base, timestamp, ext)
}

// FreeMainStorage transfers all files from a main account's sync folder to its associated backup accounts.
func (tr *TaskRunner) FreeMainStorage(ctx context.Context) error {
	logger.Info("Starting 'free main storage' process...")
	if err := tr.GetMetadata(ctx); err != nil {
		return fmt.Errorf("failed to update metadata before freeing main: %w", err)
	}

	accountsByProvider := make(map[string][]model.User)
	for _, u := range tr.Config.Users {
		accountsByProvider[u.Provider] = append(accountsByProvider[u.Provider], u)
	}

	for provider, accounts := range accountsByProvider {
		logger.Info("\n--- Processing Provider: %s ---", provider)

		var mainUser model.User
		var backupUsers []model.User
		isMainSet := false
		for _, acc := range accounts {
			if acc.IsMain {
				mainUser, isMainSet = acc, true
			} else {
				backupUsers = append(backupUsers, acc)
			}
		}

		if !isMainSet {
			logger.Info("No main account configured. Skipping.")
			continue
		}
		if len(backupUsers) == 0 {
			logger.Error("Cannot free main account %s: No backup accounts are configured for this provider.", mainUser.Email)
			continue
		}

		// Get quotas for all backup accounts
		quotas := make(map[string]*api.QuotaInfo)
		var totalBackupSpace int64
		for _, bu := range backupUsers {
			client, ok := tr.Clients[bu.Email]
			if !ok {
				logger.TaggedError(bu.Email, "Skipping, client is unavailable.")
				continue
			}
			quota, err := client.GetStorageQuota(ctx)
			if err != nil {
				return fmt.Errorf("failed to get quota for backup account %s: %w", bu.Email, err)
			}
			quotas[bu.Email] = quota
			totalBackupSpace += quota.FreeBytes
		}

		// Get all files owned by the main user and check if there's enough space
		allFiles, err := tr.DB.GetAllFilesByProvider(provider)
		if err != nil {
			return err
		}
		var filesToMove []model.File
		var totalFileSizeToMove int64
		for _, f := range allFiles {
			if f.OwnerEmail == mainUser.Email {
				filesToMove = append(filesToMove, f)
				totalFileSizeToMove += f.FileSize
			}
		}

		if len(filesToMove) == 0 {
			logger.Info("Main account %s owns no files in the sync directory. Nothing to do.", mainUser.Email)
			continue
		}
		logger.Info("Main account owns %d files totaling %d bytes. Total backup free space is %d bytes.", len(filesToMove), totalFileSizeToMove, totalBackupSpace)

		if totalFileSizeToMove > totalBackupSpace {
			return fmt.Errorf("insufficient space in backup accounts. required: %d, available: %d", totalFileSizeToMove, totalBackupSpace)
		}

		// Sort files to move smallest first to maximize success chance if an error occurs mid-way
		sort.Slice(filesToMove, func(i, j int) bool {
			return filesToMove[i].FileSize < filesToMove[j].FileSize
		})

		// Move files one by one
		for _, fileToMove := range filesToMove {
			// Find backup account with most free space *at this moment*
			var destUser model.User
			var maxFreeSpace int64 = -1
			for _, bu := range backupUsers {
				q := quotas[bu.Email]
				if q != nil && q.FreeBytes > maxFreeSpace {
					maxFreeSpace = q.FreeBytes
					destUser = bu
				}
			}

			if destUser.Email == "" {
				return errors.New("unexpected error: could not find a destination backup account despite passing space check")
			}

			// Perform the move
			err := tr.moveFileBetweenAccounts(ctx, fileToMove, mainUser, destUser)
			if err != nil {
				// Stop on the first error to avoid a partial, inconsistent state.
				return fmt.Errorf("failed to move file '%s', stopping operation: %w", fileToMove.FileName, err)
			}

			// Update local quota representation to reflect the move
			quotas[destUser.Email].FreeBytes -= fileToMove.FileSize
		}
	}

	logger.Info("\n'Free main' process complete.")
	return nil
}
