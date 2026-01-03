package task

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"cloud-drives-sync/internal/api"
	"cloud-drives-sync/internal/config"
	"cloud-drives-sync/internal/database"
	"cloud-drives-sync/internal/logger"
	"cloud-drives-sync/internal/model"
)

const (
	SyncFolderName         = "synched-cloud-drives"
	StorageThresholdHigh   = 95.0 // Percent
	StorageThresholdTarget = 90.0 // Percent
	ConflictSuffix         = "_conflict"
	MaxConcurrentDownloads = 3
)

// TaskRunner orchestrates complex operations
type TaskRunner struct {
	config   *config.Config
	db       database.Database
	log      *logger.Logger
	dryRun   bool
	password string
}

// NewTaskRunner creates a new task runner
func NewTaskRunner(cfg *config.Config, db database.Database, password string, dryRun bool) *TaskRunner {
	return &TaskRunner{
		config:   cfg,
		db:       db,
		log:      logger.New(),
		dryRun:   dryRun,
		password: password,
	}
}

// PreflightCheck verifies that exactly one synched-cloud-drives folder exists for each main account
func (tr *TaskRunner) PreflightCheck(ctx context.Context, clients map[string]api.CloudClient) error {
	tr.log.Info("Running pre-flight checks...")

	for _, provider := range []model.Provider{model.ProviderGoogle, model.ProviderMicrosoft} {
		mainAccount, err := tr.config.GetMainAccount(provider)
		if err != nil {
			// No main account for this provider, skip
			continue
		}

		client, ok := clients[mainAccount.Email]
		if !ok {
			return fmt.Errorf("client not found for main account: %s", mainAccount.Email)
		}

		// Find all folders named "synched-cloud-drives"
		folders, err := client.FindFoldersByName(ctx, SyncFolderName, false)
		if err != nil {
			return fmt.Errorf("failed to search for sync folder: %w", err)
		}

		if len(folders) == 0 {
			return fmt.Errorf("no '%s' folder found for account %s. Please create it first using 'init' command", SyncFolderName, mainAccount.Email)
		}

		if len(folders) > 1 {
			return fmt.Errorf("multiple '%s' folders found for account %s. Please resolve manually", SyncFolderName, mainAccount.Email)
		}

		// Check if folder is in root, if not, move it
		folder := folders[0]
		if folder.ParentFolderID != "" && folder.ParentFolderID != "root" {
			tr.log.Warning("Sync folder for %s is not in root, moving it...", mainAccount.Email)
			if !tr.dryRun {
				if err := client.MoveFolder(ctx, folder.FolderID, ""); err != nil {
					return fmt.Errorf("failed to move sync folder to root: %w", err)
				}
			} else {
				tr.log.DryRun("Would move folder %s to root", folder.FolderID)
			}
		}

		tr.log.Success("Pre-flight check passed for %s (%s)", mainAccount.Email, provider)
	}

	return nil
}

// GetMetadata scans all accounts and updates the local database
func (tr *TaskRunner) GetMetadata(ctx context.Context, clients map[string]api.CloudClient) error {
	tr.log.Info("Fetching metadata from all accounts...")

	for email, client := range clients {
		tr.log.Info("Scanning account: %s", email)

		// Find the sync folder
		folders, err := client.FindFoldersByName(ctx, SyncFolderName, false)
		if err != nil {
			return fmt.Errorf("failed to find sync folder for %s: %w", email, err)
		}

		if len(folders) == 0 {
			tr.log.Warning("No sync folder found for %s, skipping", email)
			continue
		}

		syncFolderID := folders[0].FolderID

		// Store the sync folder itself in the database
		syncFolderRecord := folders[0]
		syncFolderRecord.Path = ""
		syncFolderRecord.NormalizedPath = ""
		if err := tr.db.UpsertFolder(&syncFolderRecord); err != nil {
			tr.log.Error("Failed to upsert sync folder: %v", err)
		}

		// List all folders recursively
		allFolders, err := client.ListFolders(ctx, syncFolderID, true)
		if err != nil {
			return fmt.Errorf("failed to list folders for %s: %w", email, err)
		}

		tr.log.Info("Found %d folders in %s", len(allFolders), email)

		// Upsert folders to database
		for i := range allFolders {
			folder := &allFolders[i]
			if err := tr.db.UpsertFolder(folder); err != nil {
				tr.log.Error("Failed to upsert folder: %v", err)
			}
		}

		// List all files recursively
		allFiles, err := client.ListFiles(ctx, syncFolderID, true)
		if err != nil {
			return fmt.Errorf("failed to list files for %s: %w", email, err)
		}

		tr.log.Info("Found %d files in %s", len(allFiles), email)

		// Process files and compute hashes if needed
		for i := range allFiles {
			file := &allFiles[i]

			// If hash is empty, we need to download and compute it
			if file.FileHash == "" {
				tr.log.Warning("Computing hash for file: %s (ID: %s)", file.FileName, file.FileID)

				hash, algo, err := client.GetFileHash(ctx, file.FileID)
				if err != nil {
					tr.log.Error("Failed to get hash for file %s: %v", file.FileName, err)
					continue
				}

				file.FileHash = hash
				file.HashAlgorithm = algo
				tr.log.Info("Computed %s hash for %s", algo, file.FileName)
			}

			// Upsert file to database
			if err := tr.db.UpsertFile(file); err != nil {
				tr.log.Error("Failed to upsert file: %v", err)
			}
		}

		tr.log.Success("Completed scanning %s", email)
	}

	return nil
}

// CheckForDuplicates finds duplicate files within each provider
func (tr *TaskRunner) CheckForDuplicates(ctx context.Context) (map[model.Provider]map[string][]model.File, error) {
	tr.log.Info("Checking for duplicate files...")

	results := make(map[model.Provider]map[string][]model.File)

	for _, provider := range []model.Provider{model.ProviderGoogle, model.ProviderMicrosoft} {
		duplicates, err := tr.db.FindDuplicates(provider)
		if err != nil {
			return nil, fmt.Errorf("failed to find duplicates for %s: %w", provider, err)
		}

		if len(duplicates) > 0 {
			results[provider] = duplicates
			tr.log.Info("Found %d sets of duplicates in %s", len(duplicates), provider)
		} else {
			tr.log.Info("No duplicates found in %s", provider)
		}
	}

	return results, nil
}

// RemoveDuplicatesUnsafe automatically removes duplicates keeping the oldest file
func (tr *TaskRunner) RemoveDuplicatesUnsafe(ctx context.Context, clients map[string]api.CloudClient, duplicates map[model.Provider]map[string][]model.File) error {
	tr.log.Info("Removing duplicates (keeping oldest)...")

	for provider, dups := range duplicates {
		for _, files := range dups {
			if len(files) < 2 {
				continue
			}

			// Sort by creation date (oldest first)
			sort.Slice(files, func(i, j int) bool {
				return files[i].CreatedOn.Before(files[j].CreatedOn)
			})

			keepFile := files[0]
			tr.log.Info("Keeping file: %s (created: %s)", keepFile.FileName, keepFile.CreatedOn)

			// Delete all others
			for i := 1; i < len(files); i++ {
				fileToDelete := files[i]
				client, ok := clients[fileToDelete.OwnerEmail]
				if !ok {
					tr.log.Error("Client not found for %s", fileToDelete.OwnerEmail)
					continue
				}

				if tr.dryRun {
					tr.log.DryRun("Would delete file: %s (ID: %s) from %s", fileToDelete.FileName, fileToDelete.FileID, fileToDelete.OwnerEmail)
				} else {
					tr.log.Info("Deleting duplicate: %s (ID: %s) from %s", fileToDelete.FileName, fileToDelete.FileID, fileToDelete.OwnerEmail)
					if err := client.DeleteFile(ctx, fileToDelete.FileID); err != nil {
						tr.log.Error("Failed to delete file: %v", err)
						continue
					}
					if err := tr.db.DeleteFile(fileToDelete.FileID, provider); err != nil {
						tr.log.Error("Failed to remove file from DB: %v", err)
					}
				}
			}
		}
	}

	return nil
}

// SyncProviders synchronizes files between Google and Microsoft main accounts
func (tr *TaskRunner) SyncProviders(ctx context.Context, clients map[string]api.CloudClient) error {
	tr.log.Info("Synchronizing providers...")

	// Get main accounts
	googleMain, err := tr.config.GetMainAccount(model.ProviderGoogle)
	if err != nil {
		return fmt.Errorf("no Google main account configured: %w", err)
	}

	msMain, err := tr.config.GetMainAccount(model.ProviderMicrosoft)
	if err != nil {
		return fmt.Errorf("no Microsoft main account configured: %w", err)
	}

	googleClient := clients[googleMain.Email]
	msClient := clients[msMain.Email]

	// Get ALL files from both providers (not just main account)
	// For Google: files can be owned by different accounts
	// For Microsoft: backup accounts access shared folders, files show as owned by them but are actually in main's folder
	googleFiles, err := tr.db.GetAllFilesByProvider(model.ProviderGoogle)
	if err != nil {
		return fmt.Errorf("failed to get Google files: %w", err)
	}
	tr.log.Info("Found %d total files in Google Drive", len(googleFiles))

	msFiles, err := tr.db.GetAllFilesByProvider(model.ProviderMicrosoft)
	if err != nil {
		return fmt.Errorf("failed to get Microsoft files: %w", err)
	}
	tr.log.Info("Found %d total files in Microsoft OneDrive", len(msFiles))

	// Create maps by normalized path
	googleByPath := make(map[string]model.File)
	for _, f := range googleFiles {
		// Get folder path
		folder, _ := tr.db.GetFolder(f.ParentFolderID, model.ProviderGoogle)
		if folder != nil {
			path := folder.NormalizedPath + "/" + logger.NormalizePath(f.FileName)
			googleByPath[path] = f
			tr.log.Info("Google file: %s -> %s (owner: %s)", f.FileName, path, f.OwnerEmail)
		} else {
			tr.log.Warning("Could not find folder %s for Google file %s", f.ParentFolderID, f.FileName)
		}
	}

	msByPath := make(map[string]model.File)
	for _, f := range msFiles {
		folder, _ := tr.db.GetFolder(f.ParentFolderID, model.ProviderMicrosoft)
		if folder != nil {
			path := folder.NormalizedPath + "/" + logger.NormalizePath(f.FileName)
			msByPath[path] = f
			tr.log.Info("Microsoft file: %s -> %s (owner: %s)", f.FileName, path, f.OwnerEmail)
		} else {
			tr.log.Warning("Could not find folder %s for Microsoft file %s", f.ParentFolderID, f.FileName)
		}
	}

	tr.log.Info("Google files by path: %d, Microsoft files by path: %d", len(googleByPath), len(msByPath))

	// Sync: copy missing files from Google to Microsoft
	for path, gFile := range googleByPath {
		if msFile, exists := msByPath[path]; !exists {
			// Skip conflict files - they were already created during hash mismatch handling
			if strings.Contains(gFile.FileName, "_conflict_") {
				tr.log.Info("Skipping conflict file that already exists: %s", path)
				continue
			}

			tr.log.Info("File exists in Google but not Microsoft: %s", path)
			if err := tr.copyFile(ctx, googleClient, msClient, &gFile, model.ProviderMicrosoft); err != nil {
				tr.log.Error("Failed to copy file: %v", err)
			}
		} else {
			// File exists in both - compare using SHA256 hashes
			gHash, err := tr.getOrEnsureComputedHash(ctx, googleClient, &gFile)
			if err != nil {
				tr.log.Warning("Failed to compute hash for Google file %s: %v", path, err)
				continue
			}

			msHash, err := tr.getOrEnsureComputedHash(ctx, msClient, &msFile)
			if err != nil {
				tr.log.Warning("Failed to compute hash for Microsoft file %s: %v", path, err)
				continue
			}

			if gHash != msHash {
				tr.log.Warning("Hash mismatch at path %s (Google SHA256: %s, MS SHA256: %s) - creating conflict copy", path, gHash, msHash)
				if err := tr.copyFileWithConflictRename(ctx, googleClient, msClient, &gFile, model.ProviderMicrosoft); err != nil {
					tr.log.Error("Failed to handle conflict: %v", err)
				}
			} else {
				tr.log.Info("File %s already exists with matching SHA256 hash - skipping", path)
			}
		}
	}

	// Sync: copy missing files from Microsoft to Google
	for path, msFile := range msByPath {
		if gFile, exists := googleByPath[path]; !exists {
			// Skip conflict files - they were already created during hash mismatch handling
			if strings.Contains(msFile.FileName, "_conflict_") {
				tr.log.Info("Skipping conflict file that already exists: %s", path)
				continue
			}

			tr.log.Info("File exists in Microsoft but not Google: %s", path)
			if err := tr.copyFile(ctx, msClient, googleClient, &msFile, model.ProviderGoogle); err != nil {
				tr.log.Error("Failed to copy file: %v", err)
			}
		} else {
			// File exists in both - compare using SHA256 hashes
			msHash, err := tr.getOrEnsureComputedHash(ctx, msClient, &msFile)
			if err != nil {
				tr.log.Warning("Failed to compute hash for Microsoft file %s: %v", path, err)
				continue
			}

			gHash, err := tr.getOrEnsureComputedHash(ctx, googleClient, &gFile)
			if err != nil {
				tr.log.Warning("Failed to compute hash for Google file %s: %v", path, err)
				continue
			}

			if msHash != gHash {
				tr.log.Warning("Hash mismatch at path %s (MS SHA256: %s, Google SHA256: %s) - creating conflict copy", path, msHash, gHash)
				if err := tr.copyFileWithConflictRename(ctx, msClient, googleClient, &msFile, model.ProviderGoogle); err != nil {
					tr.log.Error("Failed to handle conflict: %v", err)
				}
			} else {
				tr.log.Info("File %s already exists with matching SHA256 hash - skipping", path)
			}
		}
	}

	tr.log.Success("Provider synchronization complete")

	// Adjust ownership according to provider rules
	tr.log.Info("Adjusting file and folder ownership...")
	if err := tr.adjustOwnership(ctx, clients); err != nil {
		tr.log.Error("Failed to adjust ownership: %v", err)
	}

	return nil
}

// adjustOwnership ensures proper ownership for all files and folders
// Google: Main account owns folders, backup accounts own files
// Microsoft: Backup account owns folders and files, main account is editor
func (tr *TaskRunner) adjustOwnership(ctx context.Context, clients map[string]api.CloudClient) error {
	for _, provider := range []model.Provider{model.ProviderGoogle, model.ProviderMicrosoft} {
		mainAccount, err := tr.config.GetMainAccount(provider)
		if err != nil {
			tr.log.Info("No main account for %s, skipping ownership adjustment", provider)
			continue
		}

		backupAccounts := tr.config.GetBackupAccounts(provider)
		if len(backupAccounts) == 0 {
			tr.log.Warning("No backup accounts for %s, skipping ownership adjustment", provider)
			continue
		}

		mainClient := clients[mainAccount.Email]

		// Get all folders for this provider
		folders, err := tr.db.GetAllFoldersByProvider(provider)
		if err != nil {
			tr.log.Error("Failed to get folders for %s: %v", provider, err)
			continue
		}

		// Get all files for this provider
		files, err := tr.db.GetAllFilesByProvider(provider)
		if err != nil {
			tr.log.Error("Failed to get files for %s: %v", provider, err)
			continue
		}

		if provider == model.ProviderGoogle {
			// Google: Main owns folders, backup owns files
			tr.log.Info("Adjusting Google ownership: main owns folders, backup owns files")

			// Transfer folder ownership to main account by recreating folders
			for _, folder := range folders {
				if folder.NormalizedPath == "" {
					// Skip sync root folder
					continue
				}

				if folder.OwnerEmail != mainAccount.Email {
					tr.log.Info("Transferring folder ownership: %s from %s to %s", folder.FolderName, folder.OwnerEmail, mainAccount.Email)

					// Recreate folder under correct owner
					if err := tr.transferFolderOwnership(ctx, clients, &folder, folder.OwnerEmail, mainAccount.Email, backupAccounts); err != nil {
						tr.log.Error("Failed to transfer folder %s ownership: %v", folder.FolderName, err)
					}
				} else {
					// Folder already has correct owner, just ensure it's shared
					for _, backup := range backupAccounts {
						if err := mainClient.ShareFolder(ctx, folder.FolderID, backup.Email, "writer"); err != nil {
							tr.log.Warning("Failed to share folder %s with %s: %v", folder.FolderName, backup.Email, err)
						}
					}
				}
			}

			// Transfer file ownership to backup accounts (distribute evenly)
			backupIndex := 0
			for i := range files {
				file := &files[i]
				targetBackup := backupAccounts[backupIndex%len(backupAccounts)]

				if file.OwnerEmail != targetBackup.Email {
					tr.log.Info("Transferring file ownership: %s from %s to %s", file.FileName, file.OwnerEmail, targetBackup.Email)

					// Get the current owner's client
					currentOwnerClient := clients[file.OwnerEmail]
					targetClient := clients[targetBackup.Email]

					// Use transferOrCopyFileWithPath which preserves folder structure
					if err := tr.transferOrCopyFileWithPath(ctx, currentOwnerClient, targetClient, file, targetBackup.Email); err != nil {
						tr.log.Error("Failed to transfer file %s ownership: %v", file.FileName, err)
					}
				}

				backupIndex++
			}

		} else {
			// Microsoft: Backup owns everything, main is editor
			tr.log.Info("Adjusting Microsoft ownership: backup owns all, main is editor")

			backupAccount := backupAccounts[0] // Use first backup account
			backupClient := clients[backupAccount.Email]

			// Transfer folder ownership to backup account
			for _, folder := range folders {
				if folder.NormalizedPath == "" {
					// Skip sync root folder
					continue
				}

				if folder.OwnerEmail != backupAccount.Email {
					tr.log.Warning("Folder %s owned by %s (expected %s) - OneDrive API doesn't support ownership transfer, folder is shared instead", folder.FolderName, folder.OwnerEmail, backupAccount.Email)
				} // Share with main account
				if err := backupClient.ShareFolder(ctx, folder.FolderID, mainAccount.Email, "write"); err != nil {
					tr.log.Warning("Failed to share folder %s with %s: %v", folder.FolderName, mainAccount.Email, err)
				}
			}

			// Transfer file ownership to backup account
			for i := range files {
				file := &files[i]
				if file.OwnerEmail != backupAccount.Email {
					tr.log.Info("Transferring file ownership: %s from %s to %s", file.FileName, file.OwnerEmail, backupAccount.Email)

					// Get the current owner's client
					currentOwnerClient := clients[file.OwnerEmail]

					// Use transferOrCopyFileWithPath which preserves folder structure
					if err := tr.transferOrCopyFileWithPath(ctx, currentOwnerClient, backupClient, file, backupAccount.Email); err != nil {
						tr.log.Error("Failed to transfer file %s ownership: %v", file.FileName, err)
					}
				}
			}
		}
	}

	return nil
}

// copyFile copies a file from one provider to another
func (tr *TaskRunner) copyFile(ctx context.Context, srcClient, dstClient api.CloudClient, file *model.File, dstProvider model.Provider) error {
	if tr.dryRun {
		tr.log.DryRun("Would copy file %s to %s", file.FileName, dstProvider)
		return nil
	}

	// Get source folder to recreate folder structure
	srcFolder, err := tr.db.GetFolder(file.ParentFolderID, file.Provider)
	if err != nil {
		return fmt.Errorf("failed to get source folder: %w", err)
	}

	// Get or create destination folder with same path
	var dstFolderID string
	if srcFolder != nil && srcFolder.NormalizedPath != "" {
		tr.log.Info("Recreating folder structure '%s' in %s for file %s", srcFolder.NormalizedPath, dstProvider, file.FileName)
		// Create folder hierarchy in destination
		dstFolderID, err = tr.getOrCreateFolderPath(ctx, dstClient, srcFolder.NormalizedPath, dstProvider)
		if err != nil {
			return fmt.Errorf("failed to create destination folder: %w", err)
		}
	} else {
		tr.log.Info("Uploading file %s to sync root (no subfolder)", file.FileName)
		// Upload to sync root
		folders, err := dstClient.FindFoldersByName(ctx, SyncFolderName, false)
		if err != nil || len(folders) == 0 {
			return fmt.Errorf("sync folder not found on destination")
		}
		dstFolderID = folders[0].FolderID
	}

	// Download from source
	reader, err := srcClient.DownloadFile(ctx, file.FileID)
	if err != nil {
		return fmt.Errorf("failed to download file: %w", err)
	}
	defer reader.Close()

	// Upload to destination
	uploaded, err := dstClient.UploadFile(ctx, dstFolderID, file.FileName, reader)
	if err != nil {
		return fmt.Errorf("failed to upload file: %w", err)
	}

	// Set proper ownership/permissions
	if err := tr.setFileOwnership(ctx, dstClient, uploaded.FileID, dstProvider); err != nil {
		tr.log.Warning("Failed to set ownership for %s: %v", uploaded.FileName, err)
	}

	// Save to database
	if err := tr.db.UpsertFile(uploaded); err != nil {
		tr.log.Error("Failed to save uploaded file to DB: %v", err)
	}

	tr.log.Success("Copied file: %s", file.FileName)
	return nil
}

// getOrCreateFolderPath creates folder hierarchy matching the given path
func (tr *TaskRunner) getOrCreateFolderPath(ctx context.Context, client api.CloudClient, folderPath string, provider model.Provider) (string, error) {
	// Get sync root folder
	syncFolders, err := client.FindFoldersByName(ctx, SyncFolderName, false)
	if err != nil || len(syncFolders) == 0 {
		return "", fmt.Errorf("sync folder not found")
	}
	syncRootID := syncFolders[0].FolderID

	// If path is empty, return sync root
	if folderPath == "" || folderPath == "/" {
		return syncRootID, nil
	}

	// Split path and create folders recursively
	parts := strings.Split(strings.Trim(folderPath, "/"), "/")
	parentID := syncRootID
	currentPath := ""

	for _, part := range parts {
		if part == "" {
			continue
		}

		currentPath = currentPath + "/" + part
		normalizedPath := logger.NormalizePath(currentPath)

		// Always create/get folder through API (database ID may not be accessible by current client)
		folderID, err := client.GetOrCreateFolder(ctx, part, parentID)
		if err != nil {
			return "", fmt.Errorf("failed to create folder %s: %w", part, err)
		}

		// Save to database
		dstEmail, _ := client.GetUserEmail(ctx)
		folder := &model.Folder{
			FolderID:       folderID,
			Provider:       provider,
			OwnerEmail:     dstEmail,
			FolderName:     part,
			ParentFolderID: parentID,
			Path:           currentPath,
			NormalizedPath: normalizedPath,
			LastSynced:     time.Now(),
		}
		if err := tr.db.UpsertFolder(folder); err != nil {
			tr.log.Warning("Failed to save folder to DB: %v", err)
		}

		// Set folder ownership/permissions
		if err := tr.setFolderOwnership(ctx, client, folderID, provider); err != nil {
			tr.log.Warning("Failed to set folder ownership: %v", err)
		}

		parentID = folderID
	}

	return parentID, nil
}

// setFileOwnership sets proper ownership/permissions for a file
func (tr *TaskRunner) setFileOwnership(ctx context.Context, client api.CloudClient, fileID string, provider model.Provider) error {
	mainAccount, err := tr.config.GetMainAccount(provider)
	if err != nil {
		return err
	}

	backupAccounts := tr.config.GetBackupAccounts(provider)
	currentEmail, _ := client.GetUserEmail(ctx)

	if provider == model.ProviderGoogle {
		// For Google: main account should own everything
		if currentEmail != mainAccount.Email {
			// Transfer ownership to main account
			if err := client.TransferFileOwnership(ctx, fileID, mainAccount.Email); err != nil {
				return fmt.Errorf("failed to transfer ownership: %w", err)
			}
		}
		// Backup accounts should be editors (sharing happens at folder level)
	} else {
		// For Microsoft: backup accounts should own everything
		if currentEmail == mainAccount.Email && len(backupAccounts) > 0 {
			// Transfer ownership to first backup account
			if err := client.TransferFileOwnership(ctx, fileID, backupAccounts[0].Email); err != nil {
				return fmt.Errorf("failed to transfer ownership: %w", err)
			}
		}
	}

	return nil
}

// setFolderOwnership sets proper ownership/permissions for a folder
func (tr *TaskRunner) setFolderOwnership(ctx context.Context, client api.CloudClient, folderID string, provider model.Provider) error {
	mainAccount, err := tr.config.GetMainAccount(provider)
	if err != nil {
		return err
	}

	backupAccounts := tr.config.GetBackupAccounts(provider)
	currentEmail, _ := client.GetUserEmail(ctx)

	if provider == model.ProviderGoogle {
		// For Google: main account should own folders, backups should be editors
		if currentEmail != mainAccount.Email {
			// Transfer ownership to main account
			if err := client.TransferFileOwnership(ctx, folderID, mainAccount.Email); err != nil {
				return fmt.Errorf("failed to transfer folder ownership: %w", err)
			}
		}

		// Share with backup accounts as editors
		for _, backup := range backupAccounts {
			if err := client.ShareFolder(ctx, folderID, backup.Email, "writer"); err != nil {
				tr.log.Warning("Failed to share folder with %s: %v", backup.Email, err)
			}
		}
	} else {
		// For Microsoft: backup accounts should own folders, main should be editor
		if currentEmail == mainAccount.Email && len(backupAccounts) > 0 {
			// Transfer ownership to first backup account
			if err := client.TransferFileOwnership(ctx, folderID, backupAccounts[0].Email); err != nil {
				return fmt.Errorf("failed to transfer folder ownership: %w", err)
			}

			// Share with main account as editor
			if err := client.ShareFolder(ctx, folderID, mainAccount.Email, "write"); err != nil {
				tr.log.Warning("Failed to share folder with main account: %v", err)
			}
		}
	}

	return nil
}

// computeFileHash downloads a file and computes all hash formats for cross-provider comparison
func (tr *TaskRunner) computeFileHash(ctx context.Context, client api.CloudClient, file *model.File) (*model.ComputedHash, error) {
	// Check if already computed
	existing, err := tr.db.GetComputedHash(file.FileID, file.Provider)
	if err != nil {
		return nil, fmt.Errorf("failed to check existing hash: %w", err)
	}
	if existing != nil && existing.MySha256Hash != "" {
		return existing, nil
	}

	// Download file and compute hashes
	tr.log.Info("Computing hashes for %s...", file.FileName)
	reader, err := client.DownloadFile(ctx, file.FileID)
	if err != nil {
		return nil, fmt.Errorf("failed to download file: %w", err)
	}
	defer reader.Close()

	// Create hash writers
	md5Hash := md5.New()
	sha256Hash := sha256.New()

	// Use TeeReader to compute both hashes simultaneously
	multiWriter := io.MultiWriter(md5Hash, sha256Hash)
	if _, err := io.Copy(multiWriter, reader); err != nil {
		return nil, fmt.Errorf("failed to compute hashes: %w", err)
	}

	// Get hash values
	googleMD5 := hex.EncodeToString(md5Hash.Sum(nil))
	mySha256 := hex.EncodeToString(sha256Hash.Sum(nil))

	// Microsoft hash is what's already in the file record (if from Microsoft)
	microsoftB64 := ""
	if file.Provider == model.ProviderMicrosoft {
		microsoftB64 = file.FileHash // Keep the original quickXorHash or SHA1
	}

	// Save computed hashes
	if err := tr.db.UpsertComputedHash(file.FileID, file.Provider, googleMD5, microsoftB64, mySha256); err != nil {
		tr.log.Warning("Failed to save computed hash: %v", err)
	}

	computed := &model.ComputedHash{
		FileID:           file.FileID,
		Provider:         file.Provider,
		GoogleMD5Hash:    googleMD5,
		MicrosoftB64Hash: microsoftB64,
		MySha256Hash:     mySha256,
		ComputedAt:       time.Now(),
	}

	tr.log.Info("Computed SHA256: %s", mySha256)
	return computed, nil
}

// getOrEnsureComputedHash gets or computes the hash for a file
func (tr *TaskRunner) getOrEnsureComputedHash(ctx context.Context, client api.CloudClient, file *model.File) (string, error) {
	computed, err := tr.computeFileHash(ctx, client, file)
	if err != nil {
		return "", err
	}
	return computed.MySha256Hash, nil
}

// copyFileWithConflictRename copies a file with conflict rename
func (tr *TaskRunner) copyFileWithConflictRename(ctx context.Context, srcClient, dstClient api.CloudClient, file *model.File, dstProvider model.Provider) error {
	conflictName := fmt.Sprintf("%s%s_%s", file.FileName, ConflictSuffix, time.Now().Format("2006-01-02"))

	if tr.dryRun {
		tr.log.DryRun("Would copy file %s as %s to %s", file.FileName, conflictName, dstProvider)
		return nil
	}

	reader, err := srcClient.DownloadFile(ctx, file.FileID)
	if err != nil {
		return fmt.Errorf("failed to download file: %w", err)
	}
	defer reader.Close()

	folders, err := dstClient.FindFoldersByName(ctx, SyncFolderName, false)
	if err != nil || len(folders) == 0 {
		return fmt.Errorf("sync folder not found on destination")
	}

	uploaded, err := dstClient.UploadFile(ctx, folders[0].FolderID, conflictName, reader)
	if err != nil {
		return fmt.Errorf("failed to upload conflict file: %w", err)
	}

	if err := tr.db.UpsertFile(uploaded); err != nil {
		tr.log.Error("Failed to save conflict file to DB: %v", err)
	}

	tr.log.Success("Uploaded conflict file: %s", conflictName)
	return nil
}

// BalanceStorage balances storage across accounts within a provider
func (tr *TaskRunner) BalanceStorage(ctx context.Context, clients map[string]api.CloudClient) error {
	tr.log.Info("Balancing storage...")

	for _, provider := range []model.Provider{model.ProviderGoogle, model.ProviderMicrosoft} {
		_, err := tr.config.GetMainAccount(provider)
		if err != nil {
			tr.log.Info("No main account for %s, skipping", provider)
			continue
		}

		// Get all accounts for this provider
		allAccounts := tr.config.GetAllAccounts(provider)

		// Check quotas
		var overloaded []string
		quotas := make(map[string]*model.QuotaInfo)

		for _, acc := range allAccounts {
			client := clients[acc.Email]
			quota, err := client.GetQuota(ctx)
			if err != nil {
				tr.log.Error("Failed to get quota for %s: %v", acc.Email, err)
				continue
			}

			quotas[acc.Email] = quota
			tr.log.Info("Account %s: %.2f%% used (%d/%d bytes)", acc.Email, quota.PercentageUsed, quota.UsedBytes, quota.TotalBytes)

			if quota.PercentageUsed > StorageThresholdHigh {
				overloaded = append(overloaded, acc.Email)
			}
		}

		// Balance overloaded accounts
		for _, email := range overloaded {
			tr.log.Warning("Account %s is over %.0f%% full, balancing...", email, StorageThresholdHigh)
			if err := tr.balanceAccount(ctx, clients, provider, email, quotas); err != nil {
				tr.log.Error("Failed to balance account %s: %v", email, err)
			}
		}
	}

	tr.log.Success("Storage balancing complete")
	return nil
}

// balanceAccount moves files from an overloaded account to backup accounts
func (tr *TaskRunner) balanceAccount(ctx context.Context, clients map[string]api.CloudClient, provider model.Provider, email string, quotas map[string]*model.QuotaInfo) error {
	// Get all files from this account
	files, err := tr.db.GetAllFiles(provider, email)
	if err != nil {
		return fmt.Errorf("failed to get files: %w", err)
	}

	// Sort by size (largest first)
	sort.Slice(files, func(i, j int) bool {
		return files[i].FileSize > files[j].FileSize
	})

	// Get backup accounts
	backupAccounts := tr.config.GetBackupAccounts(provider)
	if len(backupAccounts) == 0 {
		return fmt.Errorf("no backup accounts available for %s", provider)
	}

	srcClient := clients[email]
	currentQuota := quotas[email]

	// Move files until we're under the target threshold
	for _, file := range files {
		if currentQuota.PercentageUsed <= StorageThresholdTarget {
			break
		}

		// Find backup account with most free space
		var targetAccount *model.User
		var maxFreeSpace int64 = 0

		for i := range backupAccounts {
			acc := &backupAccounts[i]
			quota := quotas[acc.Email]
			if quota == nil {
				continue
			}

			freeSpace := quota.TotalBytes - quota.UsedBytes
			if freeSpace > maxFreeSpace {
				maxFreeSpace = freeSpace
				targetAccount = acc
			}
		}

		if targetAccount == nil {
			tr.log.Error("No backup account with sufficient space found")
			break
		}

		tr.log.Info("Moving file %s (%d bytes) to %s", file.FileName, file.FileSize, targetAccount.Email)

		// Try to transfer ownership
		dstClient := clients[targetAccount.Email]
		if err := tr.transferOrCopyFile(ctx, srcClient, dstClient, &file, targetAccount.Email); err != nil {
			tr.log.Error("Failed to move file: %v", err)
			continue
		}

		// Update quota estimate
		currentQuota.UsedBytes -= file.FileSize
		currentQuota.PercentageUsed = (float64(currentQuota.UsedBytes) / float64(currentQuota.TotalBytes)) * 100
	}

	return nil
}

// transferOrCopyFile attempts ownership transfer, falls back to copy/delete
func (tr *TaskRunner) transferOrCopyFile(ctx context.Context, srcClient, dstClient api.CloudClient, file *model.File, targetEmail string) error {
	if tr.dryRun {
		tr.log.DryRun("Would transfer file %s to %s", file.FileName, targetEmail)
		return nil
	}

	// Try ownership transfer first
	err := srcClient.TransferFileOwnership(ctx, file.FileID, targetEmail)
	if err == nil {
		tr.log.Success("Transferred ownership of %s to %s", file.FileName, targetEmail)
		return nil
	}

	// Fallback: download and re-upload
	tr.log.Warning("Ownership transfer not supported, using download/upload fallback for %s", file.FileName)

	reader, err := srcClient.DownloadFile(ctx, file.FileID)
	if err != nil {
		return fmt.Errorf("failed to download file: %w", err)
	}
	defer reader.Close()

	// Get sync folder on destination
	folders, err := dstClient.FindFoldersByName(ctx, SyncFolderName, false)
	if err != nil || len(folders) == 0 {
		return fmt.Errorf("sync folder not found on destination")
	}

	// Upload to destination
	uploaded, err := dstClient.UploadFile(ctx, folders[0].FolderID, file.FileName, reader)
	if err != nil {
		return fmt.Errorf("failed to upload file: %w", err)
	}

	// Delete from source
	if err := srcClient.DeleteFile(ctx, file.FileID); err != nil {
		tr.log.Error("Failed to delete source file after copy: %v", err)
	}

	// Update database
	if err := tr.db.DeleteFile(file.FileID, file.Provider); err != nil {
		tr.log.Error("Failed to remove old file from DB: %v", err)
	}
	if err := tr.db.UpsertFile(uploaded); err != nil {
		tr.log.Error("Failed to save new file to DB: %v", err)
	}

	tr.log.Success("Copied and deleted file: %s", file.FileName)
	return nil
}

// transferOrCopyFileWithPath transfers file ownership preserving folder structure
func (tr *TaskRunner) transferOrCopyFileWithPath(ctx context.Context, srcClient, dstClient api.CloudClient, file *model.File, targetEmail string) error {
	if tr.dryRun {
		tr.log.DryRun("Would transfer file %s to %s", file.FileName, targetEmail)
		return nil
	}

	// Try ownership transfer first
	err := srcClient.TransferFileOwnership(ctx, file.FileID, targetEmail)
	if err == nil {
		tr.log.Success("Transferred ownership of %s to %s via API", file.FileName, targetEmail)
		// Update database with new owner
		if err := tr.db.UpdateFileOwner(file.FileID, file.Provider, targetEmail); err != nil {
			tr.log.Error("Failed to update file %s owner in database: %v", file.FileName, err)
		}
		return nil
	}

	// Fallback: download and re-upload preserving folder structure
	tr.log.Warning("API transfer failed for %s, using download/upload fallback", file.FileName)
	tr.log.Info("[TRANSFER] Downloading file '%s' (ID: %s) from %s", file.FileName, file.FileID, file.OwnerEmail)

	// Download file
	reader, err := srcClient.DownloadFile(ctx, file.FileID)
	if err != nil {
		return fmt.Errorf("failed to download file: %w", err)
	}
	defer reader.Close()

	tr.log.Info("[TRANSFER] Successfully downloaded file '%s'", file.FileName)

	// Get the source folder to determine path
	srcFolder, err := tr.db.GetFolder(file.ParentFolderID, file.Provider)
	if err != nil {
		return fmt.Errorf("failed to get source folder: %w", err)
	}

	// Find or create matching folder on destination
	var destFolderID string
	if srcFolder != nil && srcFolder.NormalizedPath != "" {
		// Recreate complete folder structure on destination
		tr.log.Info("[TRANSFER] Recreating folder structure '%s' for file %s", srcFolder.NormalizedPath, file.FileName)

		// Get destination sync folder
		syncFolders, err := dstClient.FindFoldersByName(ctx, SyncFolderName, false)
		if err != nil || len(syncFolders) == 0 {
			return fmt.Errorf("sync folder not found on destination")
		}
		destSyncFolderID := syncFolders[0].FolderID

		// Recreate full folder path hierarchy
		destFolderID, err = tr.recreateFolderPath(ctx, dstClient, srcFolder.NormalizedPath, destSyncFolderID, file.Provider, targetEmail)
		if err != nil {
			return fmt.Errorf("failed to recreate folder path: %w", err)
		}
		tr.log.Info("[TRANSFER] Folder structure recreated, uploading to folder ID: %s", destFolderID)
	} else {
		// Upload to sync folder root
		folders, err := dstClient.FindFoldersByName(ctx, SyncFolderName, false)
		if err != nil || len(folders) == 0 {
			return fmt.Errorf("sync folder not found on destination")
		}
		destFolderID = folders[0].FolderID
		tr.log.Info("[TRANSFER] Uploading to sync folder root")
	}

	// Delete original file first if it exists (to avoid "Name already exists" error)
	tr.log.Info("[TRANSFER] Deleting original file '%s' (ID: %s) from %s before upload", file.FileName, file.FileID, file.OwnerEmail)
	if err := srcClient.DeleteFile(ctx, file.FileID); err != nil {
		tr.log.Warning("Failed to delete original file before upload: %v - attempting upload anyway", err)
	} else {
		tr.log.Success("[TRANSFER] Successfully deleted original file from %s", file.OwnerEmail)
	}

	// Upload to destination
	tr.log.Info("[TRANSFER] Uploading file '%s' to %s (folder ID: %s)", file.FileName, targetEmail, destFolderID)
	uploaded, err := dstClient.UploadFile(ctx, destFolderID, file.FileName, reader)
	if err != nil {
		return fmt.Errorf("failed to upload file: %w", err)
	}
	tr.log.Success("[TRANSFER] Successfully uploaded file '%s' (new ID: %s) to %s", file.FileName, uploaded.FileID, targetEmail)

	// Update database - remove old, add new
	if err := tr.db.DeleteFile(file.FileID, file.Provider); err != nil {
		tr.log.Error("Failed to remove old file from DB: %v", err)
	}
	if err := tr.db.UpsertFile(uploaded); err != nil {
		tr.log.Error("Failed to save new file to DB: %v", err)
	}

	tr.log.Success("✓ Transferred %s from %s to %s via download/upload", file.FileName, file.OwnerEmail, targetEmail)
	return nil
}

// recreateFolderPath recreates the complete folder hierarchy from a normalized path
func (tr *TaskRunner) recreateFolderPath(ctx context.Context, client api.CloudClient, normalizedPath, syncFolderID string, provider model.Provider, ownerEmail string) (string, error) {
	// Split the path into individual folder names
	// Example: "/folder1/folder2/folder3" -> ["folder1", "folder2", "folder3"]
	pathParts := strings.Split(strings.Trim(normalizedPath, "/"), "/")

	tr.log.Info("[FOLDER-PATH] Creating folder hierarchy: %s", normalizedPath)

	currentParentID := syncFolderID

	// Create each folder in the hierarchy
	for i, folderName := range pathParts {
		if folderName == "" {
			continue
		}

		// Check if folder already exists in database
		destFolders, err := tr.db.GetAllFoldersByProvider(provider)
		if err != nil {
			return "", fmt.Errorf("failed to get folders: %w", err)
		}

		// Build the path up to this point
		currentPath := "/" + strings.Join(pathParts[:i+1], "/")

		var foundFolderID string
		for _, f := range destFolders {
			if f.NormalizedPath == currentPath && f.OwnerEmail == ownerEmail {
				foundFolderID = f.FolderID
				tr.log.Info("[FOLDER-PATH] Folder '%s' (path: %s) already exists (ID: %s)", folderName, currentPath, foundFolderID)
				break
			}
		}

		if foundFolderID != "" {
			currentParentID = foundFolderID
		} else {
			// Create the folder
			tr.log.Info("[FOLDER-PATH] Creating folder '%s' in parent %s", folderName, currentParentID)
			newFolderID, err := client.GetOrCreateFolder(ctx, folderName, currentParentID)
			if err != nil {
				return "", fmt.Errorf("failed to create folder '%s': %w", folderName, err)
			}
			tr.log.Success("[FOLDER-PATH] Created folder '%s' (ID: %s)", folderName, newFolderID)

			// Fetch and save folder to database
			allFolders, err := client.ListFolders(ctx, currentParentID, false)
			if err == nil {
				for _, f := range allFolders {
					if f.FolderID == newFolderID {
						if err := tr.db.UpsertFolder(&f); err != nil {
							tr.log.Error("Failed to save folder to DB: %v", err)
						}
						break
					}
				}
			}

			currentParentID = newFolderID
		}
	}

	tr.log.Success("[FOLDER-PATH] Complete folder hierarchy created, final folder ID: %s", currentParentID)
	return currentParentID, nil
}

// transferFolderOwnership transfers folder ownership by recreating it under the new owner
func (tr *TaskRunner) transferFolderOwnership(ctx context.Context, clients map[string]api.CloudClient, folder *model.Folder, currentOwnerEmail, targetOwnerEmail string, shareWithAccounts []model.User) error {
	if tr.dryRun {
		tr.log.DryRun("Would transfer folder %s to %s", folder.FolderName, targetOwnerEmail)
		return nil
	}

	currentClient := clients[currentOwnerEmail]
	targetClient := clients[targetOwnerEmail]

	// Get the sync folder on target account
	syncFolders, err := targetClient.FindFoldersByName(ctx, SyncFolderName, false)
	if err != nil || len(syncFolders) == 0 {
		return fmt.Errorf("sync folder not found on target account")
	}
	targetSyncFolderID := syncFolders[0].FolderID

	// Create new folder under target owner
	tr.log.Info("Creating folder '%s' under %s", folder.FolderName, targetOwnerEmail)
	newFolderID, err := targetClient.GetOrCreateFolder(ctx, folder.FolderName, targetSyncFolderID)
	if err != nil {
		return fmt.Errorf("failed to create new folder: %w", err)
	}

	// Get all files in the old folder
	filesInFolder, err := currentClient.ListFiles(ctx, folder.FolderID, false)
	if err != nil {
		return fmt.Errorf("failed to list files in folder: %w", err)
	}

	tr.log.Info("Moving %d files from old folder to new folder", len(filesInFolder))

	// Move each file to the new folder
	for i := range filesInFolder {
		file := &filesInFolder[i]

		// Download file from old folder
		reader, err := currentClient.DownloadFile(ctx, file.FileID)
		if err != nil {
			tr.log.Error("Failed to download file %s: %v", file.FileName, err)
			continue
		}

		// Upload to new folder
		uploaded, err := targetClient.UploadFile(ctx, newFolderID, file.FileName, reader)
		reader.Close()
		if err != nil {
			tr.log.Error("Failed to upload file %s to new folder: %v", file.FileName, err)
			continue
		}

		// Delete from old folder
		if err := currentClient.DeleteFile(ctx, file.FileID); err != nil {
			tr.log.Error("Failed to delete file %s from old folder: %v", file.FileName, err)
		}

		// Update database
		if err := tr.db.DeleteFile(file.FileID, file.Provider); err != nil {
			tr.log.Error("Failed to remove old file from DB: %v", err)
		}
		if err := tr.db.UpsertFile(uploaded); err != nil {
			tr.log.Error("Failed to save new file to DB: %v", err)
		}

		tr.log.Info("Moved file %s to new folder", file.FileName)
	}

	// Get all subfolders in the old folder
	subfoldersInFolder, err := currentClient.ListFolders(ctx, folder.FolderID, false)
	if err != nil {
		return fmt.Errorf("failed to list subfolders: %w", err)
	}

	// Recursively transfer subfolders
	for i := range subfoldersInFolder {
		subfolder := &subfoldersInFolder[i]
		tr.log.Info("Recursively transferring subfolder: %s", subfolder.FolderName)

		// Create subfolder in new location
		newSubfolderID, err := targetClient.GetOrCreateFolder(ctx, subfolder.FolderName, newFolderID)
		if err != nil {
			tr.log.Error("Failed to create subfolder %s: %v", subfolder.FolderName, err)
			continue
		}

		// Update subfolder's parent to point to new folder for recursive call
		subfolderCopy := *subfolder
		subfolderCopy.ParentFolderID = newSubfolderID

		// Recursively transfer this subfolder's contents
		if err := tr.transferFolderOwnership(ctx, clients, &subfolderCopy, currentOwnerEmail, targetOwnerEmail, shareWithAccounts); err != nil {
			tr.log.Error("Failed to transfer subfolder %s: %v", subfolder.FolderName, err)
		}
	}

	// Share new folder with specified accounts
	for _, account := range shareWithAccounts {
		if err := targetClient.ShareFolder(ctx, newFolderID, account.Email, "writer"); err != nil {
			tr.log.Warning("Failed to share new folder with %s: %v", account.Email, err)
		} else {
			tr.log.Info("Shared new folder with %s", account.Email)
		}
	}

	// Delete old folder
	tr.log.Info("Deleting old folder '%s' owned by %s", folder.FolderName, currentOwnerEmail)
	if err := currentClient.DeleteFile(ctx, folder.FolderID); err != nil {
		tr.log.Error("Failed to delete old folder: %v", err)
		// Don't return error - folder contents were moved successfully
	}

	// Update database with new folder
	if err := tr.db.DeleteFolder(folder.FolderID, folder.Provider); err != nil {
		tr.log.Error("Failed to remove old folder from DB: %v", err)
	}

	// Fetch new folder details and save to database
	newFolders, err := targetClient.ListFolders(ctx, targetSyncFolderID, false)
	if err == nil {
		for _, f := range newFolders {
			if f.FolderID == newFolderID {
				if err := tr.db.UpsertFolder(&f); err != nil {
					tr.log.Error("Failed to save new folder to DB: %v", err)
				}
				break
			}
		}
	}

	tr.log.Success("Successfully transferred folder %s to %s", folder.FolderName, targetOwnerEmail)
	return nil
}

// FreeMainAccount moves all files from main account to backup accounts
func (tr *TaskRunner) FreeMainAccount(ctx context.Context, clients map[string]api.CloudClient, provider model.Provider) error {
	tr.log.Info("Freeing main account for provider: %s", provider)

	mainAccount, err := tr.config.GetMainAccount(provider)
	if err != nil {
		return fmt.Errorf("no main account for %s: %w", provider, err)
	}

	backupAccounts := tr.config.GetBackupAccounts(provider)
	if len(backupAccounts) == 0 {
		return fmt.Errorf("no backup accounts configured for %s", provider)
	}

	// Get all files from main account
	files, err := tr.db.GetAllFiles(provider, mainAccount.Email)
	if err != nil {
		return fmt.Errorf("failed to get files: %w", err)
	}

	tr.log.Info("Found %d files to move from main account", len(files))

	// Get quotas for all backup accounts
	quotas := make(map[string]*model.QuotaInfo)
	for i := range backupAccounts {
		client := clients[backupAccounts[i].Email]
		quota, err := client.GetQuota(ctx)
		if err != nil {
			tr.log.Error("Failed to get quota for %s: %v", backupAccounts[i].Email, err)
			continue
		}
		quotas[backupAccounts[i].Email] = quota
	}

	// Move each file to the backup account with most free space
	mainClient := clients[mainAccount.Email]

	for i := range files {
		file := &files[i]

		// Find backup with most free space
		var targetAccount *model.User
		var maxFreeSpace int64 = 0

		for j := range backupAccounts {
			acc := &backupAccounts[j]
			quota := quotas[acc.Email]
			if quota == nil {
				continue
			}

			freeSpace := quota.TotalBytes - quota.UsedBytes
			if freeSpace > file.FileSize && freeSpace > maxFreeSpace {
				maxFreeSpace = freeSpace
				targetAccount = acc
			}
		}

		if targetAccount == nil {
			return fmt.Errorf("insufficient space in backup accounts for file: %s", file.FileName)
		}

		tr.log.Info("Moving file %s to %s", file.FileName, targetAccount.Email)

		dstClient := clients[targetAccount.Email]
		if err := tr.transferOrCopyFile(ctx, mainClient, dstClient, file, targetAccount.Email); err != nil {
			tr.log.Error("Failed to move file: %v", err)
			continue
		}

		// Update quota estimate
		quota := quotas[targetAccount.Email]
		quota.UsedBytes += file.FileSize
		quota.PercentageUsed = (float64(quota.UsedBytes) / float64(quota.TotalBytes)) * 100
	}

	tr.log.Success("Main account freed successfully")
	return nil
}

// ShareWithMain ensures all backup accounts have access to the sync folder
func (tr *TaskRunner) ShareWithMain(ctx context.Context, clients map[string]api.CloudClient) error {
	tr.log.Info("Ensuring backup accounts have access to sync folders...")

	for _, provider := range []model.Provider{model.ProviderGoogle, model.ProviderMicrosoft} {
		mainAccount, err := tr.config.GetMainAccount(provider)
		if err != nil {
			tr.log.Info("No main account for %s, skipping", provider)
			continue
		}

		mainClient := clients[mainAccount.Email]

		// Get sync folder ID
		folders, err := mainClient.FindFoldersByName(ctx, SyncFolderName, false)
		if err != nil || len(folders) == 0 {
			tr.log.Warning("Sync folder not found for %s", mainAccount.Email)
			continue
		}

		syncFolderID := folders[0].FolderID

		// Share with each backup account
		backupAccounts := tr.config.GetBackupAccounts(provider)
		for _, backup := range backupAccounts {
			hasAccess, err := mainClient.CheckFolderPermission(ctx, syncFolderID, backup.Email)
			if err != nil {
				tr.log.Error("Failed to check permission for %s: %v", backup.Email, err)
				continue
			}

			if hasAccess {
				tr.log.Info("Backup account %s already has access", backup.Email)
				continue
			}

			if tr.dryRun {
				tr.log.DryRun("Would share sync folder with %s", backup.Email)
			} else {
				tr.log.Info("Sharing sync folder with %s", backup.Email)
				if err := mainClient.ShareFolder(ctx, syncFolderID, backup.Email, "writer"); err != nil {
					tr.log.Error("Failed to share folder: %v", err)
					continue
				}
				tr.log.Success("Shared with %s", backup.Email)
			}
		}
	}

	return nil
}

// CheckTokens validates all refresh tokens
func (tr *TaskRunner) CheckTokens(ctx context.Context, clients map[string]api.CloudClient) error {
	tr.log.Info("Checking all authentication tokens...")

	for email, client := range clients {
		// Try to get user email as a simple API call
		_, err := client.GetUserEmail(ctx)
		if err != nil {
			tr.log.Error("Token for %s is invalid or expired: %v", email, err)
		} else {
			tr.log.Success("Token for %s is valid", email)
		}
	}

	return nil
}
