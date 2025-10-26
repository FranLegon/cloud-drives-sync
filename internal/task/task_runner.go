package task

import (
	"context"
	"fmt"
	"sort"
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

		// List all folders recursively
		allFolders, err := client.ListFolders(ctx, syncFolderID, true)
		if err != nil {
			return fmt.Errorf("failed to list folders for %s: %w", email, err)
		}

		tr.log.Info("Found %d folders in %s", len(allFolders), email)

		// Upsert folders to database
		for i := range allFolders {
			if err := tr.db.UpsertFolder(&allFolders[i]); err != nil {
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

	// Get all files from both providers
	googleFiles, err := tr.db.GetAllFiles(model.ProviderGoogle, googleMain.Email)
	if err != nil {
		return fmt.Errorf("failed to get Google files: %w", err)
	}

	msFiles, err := tr.db.GetAllFiles(model.ProviderMicrosoft, msMain.Email)
	if err != nil {
		return fmt.Errorf("failed to get Microsoft files: %w", err)
	}

	// Create maps by normalized path
	googleByPath := make(map[string]model.File)
	for _, f := range googleFiles {
		// Get folder path
		folder, _ := tr.db.GetFolder(f.ParentFolderID, model.ProviderGoogle)
		if folder != nil {
			path := folder.NormalizedPath + "/" + logger.NormalizePath(f.FileName)
			googleByPath[path] = f
		}
	}

	msByPath := make(map[string]model.File)
	for _, f := range msFiles {
		folder, _ := tr.db.GetFolder(f.ParentFolderID, model.ProviderMicrosoft)
		if folder != nil {
			path := folder.NormalizedPath + "/" + logger.NormalizePath(f.FileName)
			msByPath[path] = f
		}
	}

	// Sync: copy missing files from Google to Microsoft
	for path, gFile := range googleByPath {
		if _, exists := msByPath[path]; !exists {
			tr.log.Info("File exists in Google but not Microsoft: %s", path)
			if err := tr.copyFile(ctx, googleClient, msClient, &gFile, model.ProviderMicrosoft); err != nil {
				tr.log.Error("Failed to copy file: %v", err)
			}
		} else {
			// Check for conflicts (same path, different hash)
			msFile := msByPath[path]
			if gFile.FileHash != msFile.FileHash {
				tr.log.Warning("Conflict detected at path: %s", path)
				// Rename and copy
				if err := tr.copyFileWithConflictRename(ctx, googleClient, msClient, &gFile, model.ProviderMicrosoft); err != nil {
					tr.log.Error("Failed to handle conflict: %v", err)
				}
			}
		}
	}

	// Sync: copy missing files from Microsoft to Google
	for path, msFile := range msByPath {
		if _, exists := googleByPath[path]; !exists {
			tr.log.Info("File exists in Microsoft but not Google: %s", path)
			if err := tr.copyFile(ctx, msClient, googleClient, &msFile, model.ProviderGoogle); err != nil {
				tr.log.Error("Failed to copy file: %v", err)
			}
		}
	}

	tr.log.Success("Provider synchronization complete")
	return nil
}

// copyFile copies a file from one provider to another
func (tr *TaskRunner) copyFile(ctx context.Context, srcClient, dstClient api.CloudClient, file *model.File, dstProvider model.Provider) error {
	if tr.dryRun {
		tr.log.DryRun("Would copy file %s to %s", file.FileName, dstProvider)
		return nil
	}

	// Download from source
	reader, err := srcClient.DownloadFile(ctx, file.FileID)
	if err != nil {
		return fmt.Errorf("failed to download file: %w", err)
	}
	defer reader.Close()

	// Get destination folder (create if needed)
	// Simplified: upload to sync root for now
	folders, err := dstClient.FindFoldersByName(ctx, SyncFolderName, false)
	if err != nil || len(folders) == 0 {
		return fmt.Errorf("sync folder not found on destination")
	}

	// Upload to destination
	uploaded, err := dstClient.UploadFile(ctx, folders[0].FolderID, file.FileName, reader)
	if err != nil {
		return fmt.Errorf("failed to upload file: %w", err)
	}

	// Save to database
	if err := tr.db.UpsertFile(uploaded); err != nil {
		tr.log.Error("Failed to save uploaded file to DB: %v", err)
	}

	tr.log.Success("Copied file: %s", file.FileName)
	return nil
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
