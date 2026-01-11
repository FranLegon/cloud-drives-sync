package task

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/FranLegon/cloud-drives-sync/internal/api"
	"github.com/FranLegon/cloud-drives-sync/internal/auth"
	"github.com/FranLegon/cloud-drives-sync/internal/database"
	"github.com/FranLegon/cloud-drives-sync/internal/google"
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/FranLegon/cloud-drives-sync/internal/microsoft"
	"github.com/FranLegon/cloud-drives-sync/internal/model"
	"github.com/FranLegon/cloud-drives-sync/internal/telegram"
)

// Runner handles task orchestration
type Runner struct {
	config   *model.Config
	db       *database.DB
	safeMode bool
	clients  map[string]api.CloudClient
}

// NewRunner creates a new task runner
func NewRunner(config *model.Config, db *database.DB, safeMode bool) *Runner {
	return &Runner{
		config:   config,
		db:       db,
		safeMode: safeMode,
		clients:  make(map[string]api.CloudClient),
	}
}

// GetOrCreateClient gets or creates a client for a user
func (r *Runner) GetOrCreateClient(user *model.User) (api.CloudClient, error) {
	key := string(user.Provider) + ":" + user.Email + user.Phone

	if client, exists := r.clients[key]; exists {
		return client, nil
	}

	var client api.CloudClient
	var err error

	switch user.Provider {
	case model.ProviderGoogle:
		config := auth.GetGoogleOAuthConfig(r.config.GoogleClient.ID, r.config.GoogleClient.Secret)
		client, err = google.NewClient(user, config)
	case model.ProviderMicrosoft:
		config := auth.GetMicrosoftOAuthConfig(r.config.MicrosoftClient.ID, r.config.MicrosoftClient.Secret)
		client, err = microsoft.NewClient(user, config)
	case model.ProviderTelegram:
		client, err = telegram.NewClient(user, r.config.TelegramClient.APIID, r.config.TelegramClient.APIHash)
	default:
		return nil, fmt.Errorf("unsupported provider: %s", user.Provider)
	}

	if err != nil {
		return nil, err
	}

	r.clients[key] = client
	return client, nil
}

// RunPreFlightChecks runs pre-flight checks for all accounts
func (r *Runner) RunPreFlightChecks() error {
	logger.Info("Running pre-flight checks...")

	for i := range r.config.Users {
		user := &r.config.Users[i]
		client, err := r.GetOrCreateClient(user)
		if err != nil {
			return fmt.Errorf("failed to create client for %s: %w", user.Email+user.Phone, err)
		}

		if err := client.PreFlightCheck(); err != nil {
			return fmt.Errorf("pre-flight check failed for %s: %w", user.Email+user.Phone, err)
		}
	}

	logger.Info("All pre-flight checks passed")
	return nil
}

// GetMetadata scans all providers and updates the database
func (r *Runner) GetMetadata() error {
	logger.Info("Gathering metadata from all providers...")

	fileChan := make(chan *model.File, 1000)
	folderChan := make(chan *model.Folder, 1000)

	// Start DB writer
	var dbWg sync.WaitGroup
	dbWg.Add(1)
	go func() {
		defer dbWg.Done()
		r.dbWriter(fileChan, folderChan)
	}()

	var wg sync.WaitGroup

	for i := range r.config.Users {
		wg.Add(1)
		go func(user *model.User) {
			defer wg.Done()

			client, err := r.GetOrCreateClient(user)
			if err != nil {
				logger.ErrorTagged([]string{string(user.Provider)}, "Failed to create client: %v", err)
				return
			}

			// Get sync folder ID
			syncFolderID, err := client.GetSyncFolderID()
			if err != nil {
				logger.ErrorTagged([]string{string(user.Provider), user.Email + user.Phone}, "Failed to get sync folder: %v", err)
				return
			}

			if syncFolderID == "" {
				logger.InfoTagged([]string{string(user.Provider), user.Email + user.Phone}, "No sync folder, skipping")
				return
			}

			// Scan files
			if err := r.scanFolder(client, user, syncFolderID, "", fileChan, folderChan); err != nil {
				logger.ErrorTagged([]string{string(user.Provider), user.Email + user.Phone}, "Failed to scan folder: %v", err)
			}
		}(&r.config.Users[i])
	}

	wg.Wait()
	close(fileChan)
	close(folderChan)
	dbWg.Wait()

	logger.Info("Metadata gathering complete")
	return nil
}

func (r *Runner) dbWriter(fileChan <-chan *model.File, folderChan <-chan *model.Folder) {
	const batchSize = 500
	const flushInterval = 2 * time.Second

	var fileBuffer []*model.File
	var folderBuffer []*model.Folder

	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	flushFiles := func() {
		if len(fileBuffer) > 0 {
			if err := r.db.BatchInsertFiles(fileBuffer); err != nil {
				logger.Error("Failed to batch insert files: %v", err)
			}
			fileBuffer = nil
		}
	}

	flushFolders := func() {
		if len(folderBuffer) > 0 {
			if err := r.db.BatchInsertFolders(folderBuffer); err != nil {
				logger.Error("Failed to batch insert folders: %v", err)
			}
			folderBuffer = nil
		}
	}

	for {
		select {
		case file, ok := <-fileChan:
			if !ok {
				fileChan = nil
			} else {
				fileBuffer = append(fileBuffer, file)
				if len(fileBuffer) >= batchSize {
					flushFiles()
				}
			}
		case folder, ok := <-folderChan:
			if !ok {
				folderChan = nil
			} else {
				folderBuffer = append(folderBuffer, folder)
				if len(folderBuffer) >= batchSize {
					flushFolders()
				}
			}
		case <-ticker.C:
			flushFiles()
			flushFolders()
		}

		if fileChan == nil && folderChan == nil {
			break
		}
	}

	// Final flush
	flushFiles()
	flushFolders()
}

func (r *Runner) scanFolder(client api.CloudClient, user *model.User, folderID, pathPrefix string, fileChan chan<- *model.File, folderChan chan<- *model.Folder) error {
	// List and store files
	files, err := client.ListFiles(folderID)
	if err != nil {
		return err
	}

	for _, file := range files {
		file.Path = pathPrefix + "/" + file.Name
		fileChan <- file
	}

	logger.InfoTagged([]string{string(user.Provider), user.Email + user.Phone}, "Found %d files in folder %s", len(files), folderID)

	// Recursively scan subfolders
	folders, err := client.ListFolders(folderID)
	if err != nil {
		return err
	}

	for _, folder := range folders {
		// Skip sync-cloud-drives-aux folder
		if folder.Name == "sync-cloud-drives-aux" {
			continue
		}

		folder.Path = pathPrefix + "/" + folder.Name
		folderChan <- folder

		// Recurse
		if err := r.scanFolder(client, user, folder.ID, folder.Path, fileChan, folderChan); err != nil {
			return err
		}
	}

	return nil
}

// CheckForDuplicates finds duplicate files within each provider
func (r *Runner) CheckForDuplicates() error {
	logger.Info("Checking for duplicate files...")

	// Note: In the normalized schema, duplicates are at the file level, not provider level
	// Each file can have multiple replicas across providers
	ids, err := r.db.GetDuplicateCalculatedIDs()
	if err != nil {
		logger.Error("Failed to query duplicates: %v", err)
		return err
	}

	if len(ids) == 0 {
		logger.Info("No duplicates found")
		return nil
	}

	foundDuplicates := false

	for _, calculatedID := range ids {
		files, err := r.db.GetFilesByCalculatedID(calculatedID)
		if err != nil {
			logger.Error("Failed to get files for calculated_id %s: %v", calculatedID, err)
			continue
		}

		if len(files) > 1 {
			foundDuplicates = true
			fmt.Printf("\nDuplicate files (CalculatedID: %s):\n", calculatedID)
			for i, file := range files {
				// Show replicas for each file
				providerList := []string{}
				for _, replica := range file.Replicas {
					providerList = append(providerList, fmt.Sprintf("%s(%s)", replica.Provider, replica.AccountID))
				}
				fmt.Printf("  %d. %s (ID: %s, Size: %d, ModTime: %s, Providers: %v)\n",
					i+1, file.Path, file.ID, file.Size, file.ModTime.Format("2006-01-02"), providerList)
			}
		}
	}

	if !foundDuplicates {
		logger.Info("No duplicates found")
	}

	return nil
}

// CheckTokens validates all tokens
func (r *Runner) CheckTokens() error {
	logger.Info("Checking all authentication tokens...")

	hasErrors := false

	for i := range r.config.Users {
		user := &r.config.Users[i]
		client, err := r.GetOrCreateClient(user)
		if err != nil {
			logger.ErrorTagged([]string{string(user.Provider), user.Email + user.Phone}, "Failed to create client: %v", err)
			hasErrors = true
			continue
		}

		// Try a simple read operation
		_, err = client.GetQuota()
		if err != nil {
			logger.ErrorTagged([]string{string(user.Provider), user.Email + user.Phone}, "Token validation failed: %v", err)
			hasErrors = true
		} else {
			logger.InfoTagged([]string{string(user.Provider), user.Email + user.Phone}, "Token is valid")
		}
	}

	if hasErrors {
		return fmt.Errorf("some tokens are invalid - re-authentication required")
	}

	logger.Info("All tokens are valid")
	return nil
}

// ShareWithMain repairs sharing permissions
func (r *Runner) ShareWithMain() error {
	logger.Info("Verifying and repairing share permissions...")

	// For Google: ensure backup accounts have access to main folder
	googleMain := getMainAccount(r.config, model.ProviderGoogle)
	if googleMain != nil {
		client, err := r.GetOrCreateClient(googleMain)
		if err != nil {
			return err
		}

		syncFolderID, err := client.GetSyncFolderID()
		if err != nil {
			return err
		}

		backupAccounts := getBackupAccounts(r.config, model.ProviderGoogle)
		for _, backup := range backupAccounts {
			if !r.safeMode {
				if err := client.ShareFolder(syncFolderID, backup.Email, "writer"); err != nil {
					logger.WarningTagged([]string{"Google", googleMain.Email}, "Failed to share with %s: %v", backup.Email, err)
				} else {
					logger.InfoTagged([]string{"Google", googleMain.Email}, "Shared folder with %s", backup.Email)
				}
			} else {
				logger.DryRunTagged([]string{"Google", googleMain.Email}, "Would share folder with %s", backup.Email)
			}
		}
	}

	// For Microsoft: ensure backup folders are shared with main
	// Implementation would be similar

	logger.Info("Share permissions verified")
	return nil
}

func getMainAccount(config *model.Config, provider model.Provider) *model.User {
	for i := range config.Users {
		if config.Users[i].Provider == provider && config.Users[i].IsMain {
			return &config.Users[i]
		}
	}
	return nil
}

func getBackupAccounts(config *model.Config, provider model.Provider) []model.User {
	var accounts []model.User
	for _, user := range config.Users {
		if user.Provider == provider && !user.IsMain {
			accounts = append(accounts, user)
		}
	}
	return accounts
}

// BalanceStorage checks quotas and moves files to balance storage
func (r *Runner) BalanceStorage() error {
	logger.Info("Balancing storage across accounts...")

	// Group users by provider
	usersByProvider := make(map[model.Provider][]model.User)
	for _, user := range r.config.Users {
		// Skip main account for balancing - only balance among backup accounts
		if user.IsMain {
			continue
		}
		usersByProvider[user.Provider] = append(usersByProvider[user.Provider], user)
	}

	for provider, users := range usersByProvider {
		if provider != model.ProviderGoogle {
			logger.Info("Skipping %s (not supported for balancing)", provider)
			continue
		}

		logger.Info("Checking quotas for %s...", provider)

		type AccountStatus struct {
			User     model.User
			Client   api.CloudClient
			Quota    *api.QuotaInfo
			UsagePct float64
		}

		var sources []*AccountStatus
		var targets []*AccountStatus

		for _, user := range users {
			client, err := r.GetOrCreateClient(&user)
			if err != nil {
				logger.Error("Failed to create client for %s: %v", user.Email, err)
				continue
			}

			quota, err := client.GetQuota()
			if err != nil {
				logger.Error("Failed to get quota for %s: %v", user.Email, err)
				continue
			}

			usagePct := float64(quota.Used) / float64(quota.Total) * 100
			status := &AccountStatus{
				User:     user,
				Client:   client,
				Quota:    quota,
				UsagePct: usagePct,
			}

			logger.InfoTagged([]string{string(provider), user.Email}, "Usage: %.2f%% (%d/%d bytes)", usagePct, quota.Used, quota.Total)

			if usagePct > 95.0 {
				sources = append(sources, status)
			} else if usagePct < 90.0 {
				targets = append(targets, status)
			}
		}

		if len(sources) == 0 {
			logger.Info("No accounts over quota for %s", provider)
			continue
		}

		if len(targets) == 0 {
			logger.Warning("No accounts with free space available for %s", provider)
			continue
		}

		// Sort targets by most free space (descending)
		sort.Slice(targets, func(i, j int) bool {
			freeI := targets[i].Quota.Total - targets[i].Quota.Used
			freeJ := targets[j].Quota.Total - targets[j].Quota.Used
			return freeI > freeJ
		})

		// Process sources
		for _, source := range sources {
			logger.InfoTagged([]string{string(provider), source.User.Email}, "Account is over quota, looking for files to move...")

			syncFolderID, err := source.Client.GetSyncFolderID()
			if err != nil {
				logger.Error("Failed to get sync folder: %v", err)
				continue
			}

			files, err := r.getAllFilesRecursive(source.Client, syncFolderID)
			if err != nil {
				logger.Error("Failed to list files recursively: %v", err)
				continue
			}

			// Filter files owned by user and sort by size (descending)
			var candidates []*model.File
			for _, f := range files {
				// Check ownership (Google Drive specific check)
				if f.OwnerEmail == source.User.Email {
					candidates = append(candidates, f)
				}
			}

			sort.Slice(candidates, func(i, j int) bool {
				return candidates[i].Size > candidates[j].Size
			})

			for _, file := range candidates {
				// Stop if source is safe
				if source.UsagePct < 90.0 {
					logger.InfoTagged([]string{string(provider), source.User.Email}, "Account is now under safe threshold")
					break
				}

				// Find a target with enough space
				var target *AccountStatus
				for _, t := range targets {
					freeSpace := t.Quota.Total - t.Quota.Used
					if freeSpace > file.Size {
						target = t
						break
					}
				}

				if target == nil {
					logger.Warning("No target account has enough space for file %s (%d bytes)", file.Name, file.Size)
					continue
				}

				// Move file (Transfer Ownership)
				if !r.safeMode {
					logger.InfoTagged([]string{string(provider), source.User.Email}, "Transferring %s (%d bytes) to %s", file.Name, file.Size, target.User.Email)
					err := source.Client.TransferOwnership(file.ID, target.User.Email)
					if err != nil {
						if err == api.ErrOwnershipTransferPending {
							logger.InfoTagged([]string{string(provider), source.User.Email}, "Ownership transfer pending, accepting as %s...", target.User.Email)
							if err := target.Client.AcceptOwnership(file.ID); err != nil {
								logger.Error("Failed to accept ownership: %v", err)
								continue
							}
						} else {
							logger.Error("Failed to transfer ownership: %v", err)
							continue
						}
					}
				} else {
					logger.DryRunTagged([]string{string(provider), source.User.Email}, "Would transfer %s (%d bytes) to %s", file.Name, file.Size, target.User.Email)
				}

				// Update local quotas
				source.Quota.Used -= file.Size
				source.UsagePct = float64(source.Quota.Used) / float64(source.Quota.Total) * 100

				target.Quota.Used += file.Size
				target.UsagePct = float64(target.Quota.Used) / float64(target.Quota.Total) * 100
			}
		}
	}
	return nil
}

// FreeMain transfers all files from main account to backup accounts
func (r *Runner) FreeMain() error {
	logger.Info("Freeing up main account storage...")

	// Find main account (Google)
	var mainUser *model.User
	for i := range r.config.Users {
		if r.config.Users[i].IsMain && r.config.Users[i].Provider == model.ProviderGoogle {
			mainUser = &r.config.Users[i]
			break
		}
	}

	if mainUser == nil {
		logger.Warning("No Google main account found")
		return nil
	}

	// Find backup accounts for Google
	var backupUsers []model.User
	for _, user := range r.config.Users {
		if !user.IsMain && user.Provider == model.ProviderGoogle {
			backupUsers = append(backupUsers, user)
		}
	}

	if len(backupUsers) == 0 {
		logger.Warning("No Google backup accounts found")
		return nil
	}

	logger.Info("Processing Google (Main: %s)", mainUser.Email)

	// Initialize main client
	mainClient, err := r.GetOrCreateClient(mainUser)
	if err != nil {
		logger.Error("Failed to create client for main account: %v", err)
		return err
	}

	// Get backup accounts status
	type BackupStatus struct {
		User   model.User
		Client api.CloudClient
		Quota  *api.QuotaInfo
		Free   int64
	}

	var targets []*BackupStatus

	for _, user := range backupUsers {
		client, err := r.GetOrCreateClient(&user)
		if err != nil {
			logger.Error("Failed to create client for %s: %v", user.Email, err)
			continue
		}

		quota, err := client.GetQuota()
		if err != nil {
			logger.Error("Failed to get quota for %s: %v", user.Email, err)
			continue
		}

		free := quota.Total - quota.Used
		// Only consider accounts with some free space
		if free > 0 {
			targets = append(targets, &BackupStatus{
				User:   user,
				Client: client,
				Quota:  quota,
				Free:   free,
			})
		}
	}

	if len(targets) == 0 {
		logger.Warning("No backup accounts with free space available for Google")
		return nil
	}

	// List files in main account
	syncFolderID, err := mainClient.GetSyncFolderID()
	if err != nil {
		logger.Error("Failed to get sync folder: %v", err)
		return err
	}

	files, err := r.getAllFilesRecursive(mainClient, syncFolderID)
	if err != nil {
		logger.Error("Failed to list files recursively: %v", err)
		return err
	}

	// Filter files owned by main user
	var candidates []*model.File
	for _, f := range files {
		if f.OwnerEmail == mainUser.Email {
			candidates = append(candidates, f)
		}
	}

	if len(candidates) == 0 {
		logger.Info("No files found in main account to move")
		return nil
	}

	// Sort files by size (descending) to move big chunks first
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Size > candidates[j].Size
	})

	// Move files
	for _, file := range candidates {
		// Sort targets by free space (descending) - re-sort each time as space changes
		sort.Slice(targets, func(i, j int) bool {
			return targets[i].Free > targets[j].Free
		})

		// Find best target
		var target *BackupStatus
		for _, t := range targets {
			if t.Free > file.Size {
				target = t
				break
			}
		}

		if target == nil {
			logger.Warning("No backup account has enough space for file %s (%d bytes)", file.Name, file.Size)
			continue
		}

		// Move file
		if !r.safeMode {
			logger.InfoTagged([]string{"Google", mainUser.Email}, "Transferring %s (%d bytes) to %s", file.Name, file.Size, target.User.Email)
			err := mainClient.TransferOwnership(file.ID, target.User.Email)
			if err != nil {
				if err == api.ErrOwnershipTransferPending {
					logger.InfoTagged([]string{"Google", mainUser.Email}, "Ownership transfer pending, accepting as %s...", target.User.Email)
					if err := target.Client.AcceptOwnership(file.ID); err != nil {
						logger.Error("Failed to accept ownership: %v", err)
						continue
					}
				} else {
					logger.Error("Failed to transfer ownership: %v", err)
					continue
				}
			}
		} else {
			logger.DryRunTagged([]string{"Google", mainUser.Email}, "Would transfer %s (%d bytes) to %s", file.Name, file.Size, target.User.Email)
		}

		// Update local state
		target.Free -= file.Size
		target.Quota.Used += file.Size
	}

	return nil
}

// getAllFilesRecursive recursively lists all files in a folder and its subfolders
func (r *Runner) getAllFilesRecursive(client api.CloudClient, folderID string) ([]*model.File, error) {
	var allFiles []*model.File

	// List files in current folder
	files, err := client.ListFiles(folderID)
	if err != nil {
		return nil, err
	}
	allFiles = append(allFiles, files...)

	// List subfolders
	folders, err := client.ListFolders(folderID)
	if err != nil {
		return nil, err
	}

	// Recursively list files in subfolders
	for _, folder := range folders {
		subFiles, err := r.getAllFilesRecursive(client, folder.ID)
		if err != nil {
			return nil, err
		}
		allFiles = append(allFiles, subFiles...)
	}

	return allFiles, nil
}

// SyncProviders synchronizes files across all providers
func (r *Runner) SyncProviders() error {
	logger.Info("Synchronizing providers...")

	// Get all files
	files, err := r.db.GetAllFilesAcrossProviders()
	if err != nil {
		return fmt.Errorf("failed to get files: %w", err)
	}

	// Group by normalized path
	filesByPath := make(map[string]map[model.Provider]*model.File)
	for _, f := range files {
		if _, ok := filesByPath[f.Path]; !ok {
			filesByPath[f.Path] = make(map[model.Provider]*model.File)
		}
		filesByPath[f.Path][f.Provider] = f
	}

	// Check for missing files
	providers := []model.Provider{model.ProviderGoogle, model.ProviderMicrosoft, model.ProviderTelegram}
	softDeletedPath := "sync-cloud-drives-aux/soft-deleted"

	for path, fileMap := range filesByPath {
		// Determine master file (prioritize Google, then Microsoft, then Telegram)
		var masterFile *model.File

		// Check if any file is in soft-deleted folder
		isSoftDeleted := false
		if strings.Contains(path, softDeletedPath) {
			isSoftDeleted = true
		}

		// Logic for "If a file is found in this special folder for one provider and in another folder for another provider,
		// it must be moved to sync-cloud-drives-aux/soft-deleted for all providers."
		// However, r.db.GetAllFilesAcrossProviders grouped by Path. So if it is in a different folder,
		// it will be treated as a different entry in filesByPath.
		// We need to inspect file content identity (CalculatedID) to catch moves, but that is complex across providers.
		// For now, based strictly on requirements: "sync-providers should not sync this folder, just ignore it."
		// But "If a file is found in this special folder for one provider and in another folder for another provider"
		// This implies we need to look for duplicates across the map or iterate.
		// Since CalculatedIDs might differ if not hashed, we rely on file name + size or weak hash if strong hash absent.

		if isSoftDeleted {
			// Ignore syncing creating files in this folder for other providers if missing?
			// Requirement: "sync-providers should not sync this folder, just ignore it."
			// BUT "If a file is found in this special folder for one provider and in another folder for another provider, it must be moved to sync-cloud-drives-aux/soft-deleted for all providers."

			// Let's implement the move logic first. find identical files in other locations.
			// We iterate through all OTHER entries in filesByPath to find matching files.
			// This is O(N^2) which is bad, but N is number of unique files.
			// Better: map {CalculatedID -> []paths}
		}

		if f, ok := fileMap[model.ProviderGoogle]; ok {
			masterFile = f
		} else if f, ok := fileMap[model.ProviderMicrosoft]; ok {
			masterFile = f
		} else if f, ok := fileMap[model.ProviderTelegram]; ok {
			masterFile = f
		}

		if masterFile == nil {
			continue
		}

		// Skip syncing if this is the soft deleted folder
		if isSoftDeleted {
			continue
		}

		for _, provider := range providers {
			if _, exists := fileMap[provider]; !exists {
				// File missing in this provider
				logger.Info("File %s missing in %s", path, provider)

				if !r.safeMode {
					if err := r.copyFile(masterFile, provider, ""); err != nil {
						logger.Error("Failed to copy file: %v", err)
					}
				} else {
					logger.DryRun("Would copy %s from %s to %s", path, masterFile.Provider, provider)
				}
			} else {
				// File exists, check calculated ID for conflict
				existingFile := fileMap[provider]
				if existingFile.CalculatedID != masterFile.CalculatedID {
					logger.Warning("Conflict detected for %s in %s (CalculatedID mismatch)", path, provider)

					// Generate conflict name
					ext := filepath.Ext(masterFile.Name)
					nameWithoutExt := strings.TrimSuffix(masterFile.Name, ext)
					timestamp := time.Now().Format("2006-01-02_15-04-05")
					conflictName := fmt.Sprintf("%s_conflict_%s%s", nameWithoutExt, timestamp, ext)

					if !r.safeMode {
						logger.Info("Resolving conflict by uploading as %s", conflictName)
						if err := r.copyFile(masterFile, provider, conflictName); err != nil {
							logger.Error("Failed to resolve conflict: %v", err)
						}
					} else {
						logger.DryRun("Would resolve conflict by uploading %s as %s to %s", path, conflictName, provider)
					}
				}
			}
		}
	}

	// Check for soft-delete consistency
	if err := r.checkSoftDeletedConsistency(filesByPath, softDeletedPath); err != nil {
		logger.Error("Failed to check soft deleted consistency: %v", err)
	}

	// Phase 2: Distribute Shortcuts for Microsoft OneDrive
	if err := r.distributeShortcuts(); err != nil {
		logger.Error("Failed to distribute shortcuts: %v", err)
	}

	return nil
}

// distributeShortcuts ensures that for every file in Microsoft OneDrive,
// all other OneDrive accounts have a shortcut to it.
func (r *Runner) distributeShortcuts() error {
	logger.Info("Distributing OneDrive shortcuts...")

	files, err := r.db.GetAllFilesAcrossProviders()
	if err != nil {
		return err
	}

	// Group files by Path
	filesByPath := make(map[string][]*model.File)
	for _, f := range files {
		filesByPath[f.Path] = append(filesByPath[f.Path], f)
	}

	// Identify all Microsoft accounts
	var msUsers []model.User
	for _, u := range r.config.Users {
		if u.Provider == model.ProviderMicrosoft {
			msUsers = append(msUsers, u)
		}
	}

	if len(msUsers) < 2 {
		return nil // No need to distribute if only 0 or 1 MS account
	}

	for path, pathFiles := range filesByPath {
		// Check if this path exists in Microsoft
		var msFiles []*model.File
		for _, f := range pathFiles {
			if f.Provider == model.ProviderMicrosoft {
				msFiles = append(msFiles, f)
			}
		}

		if len(msFiles) == 0 {
			continue
		}

		// Pick a source file (preferably one that's not a shortcut if we knew, otherwise first)
		sourceFile := msFiles[0]

		// Ensure all other MS users have it
		for _, user := range msUsers {
			// Check if user has it
			hasIt := false
			for _, f := range msFiles {
				if f.UserEmail == user.Email {
					hasIt = true
					break
				}
			}

			if !hasIt {
				if !r.safeMode {
					if err := r.createShortcut(sourceFile, &user); err != nil {
						logger.Error("Failed to create shortcut for %s in %s: %v", path, user.Email, err)
					}
				} else {
					logger.DryRun("Would create shortcut for %s in %s -> %s", path, user.Email, sourceFile.UserEmail)
				}
			}
		}
	}
	return nil
}

// DeleteUnsyncedFiles deletes files in backup accounts that are not in the sync folder
func (r *Runner) DeleteUnsyncedFiles() error {
	for _, user := range r.config.Users {
		// Skip main account
		if user.IsMain {
			continue
		}

		// Skip Telegram for now as it doesn't have a root folder structure
		if user.Provider == model.ProviderTelegram {
			continue
		}

		logger.InfoTagged([]string{string(user.Provider), user.Email}, "Checking for unsynced files...")

		client, err := r.GetOrCreateClient(&user)
		if err != nil {
			logger.Error("Failed to create client for %s: %v", user.Email, err)
			continue
		}

		// Get sync folder ID
		syncFolderID, err := client.GetSyncFolderID()
		if err != nil {
			logger.Error("Failed to get sync folder for %s: %v", user.Email, err)
			continue
		}

		// List folders in root
		folders, err := client.ListFolders("root")
		if err != nil {
			logger.Error("Failed to list folders for %s: %v", user.Email, err)
			continue
		}

		for _, folder := range folders {
			if folder.ID != syncFolderID {
				if !r.safeMode {
					logger.InfoTagged([]string{string(user.Provider), user.Email}, "Deleting unsynced folder: %s", folder.Name)
					if err := client.DeleteFolder(folder.ID); err != nil {
						logger.Error("Failed to delete folder %s: %v", folder.Name, err)
					}
				} else {
					logger.DryRunTagged([]string{string(user.Provider), user.Email}, "Would delete unsynced folder: %s", folder.Name)
				}
			}
		}

		// List files in root
		files, err := client.ListFiles("root")
		if err != nil {
			logger.Error("Failed to list files for %s: %v", user.Email, err)
			continue
		}

		for _, file := range files {
			if !r.safeMode {
				logger.InfoTagged([]string{string(user.Provider), user.Email}, "Deleting unsynced file: %s", file.Name)
				if err := client.DeleteFile(file.ID); err != nil {
					logger.Error("Failed to delete file %s: %v", file.Name, err)
				}
			} else {
				logger.DryRunTagged([]string{string(user.Provider), user.Email}, "Would delete unsynced file: %s", file.Name)
			}
		}
	}
	return nil
}

// GetProviderQuotas calculates aggregated quotas for all providers
func (r *Runner) GetProviderQuotas() ([]*model.ProviderQuota, error) {
	logger.Info("Calculating provider quotas...")

	quotas := make(map[model.Provider]*model.ProviderQuota)

	// Initialize map
	for _, p := range []model.Provider{model.ProviderGoogle, model.ProviderMicrosoft, model.ProviderTelegram} {
		quotas[p] = &model.ProviderQuota{
			Provider: p,
			Total:    0,
			Used:     0,
			Free:     0,
		}
	}

	for i := range r.config.Users {
		user := &r.config.Users[i]
		client, err := r.GetOrCreateClient(user)
		if err != nil {
			return nil, fmt.Errorf("failed to create client for %s: %w", user.Email+user.Phone, err)
		}

		q, err := client.GetQuota()
		if err != nil {
			return nil, fmt.Errorf("failed to get quota for %s: %w", user.Email+user.Phone, err)
		}

		pq := quotas[user.Provider]

		// Aggregate Used
		pq.Used += q.Used

		// Aggregate Total and Free (skip for main account)
		if !user.IsMain {
			if pq.Total == -1 {
				// Already unlimited, stay unlimited
			} else if q.Total == -1 {
				// Found an unlimited account, set provider to unlimited
				pq.Total = -1
				pq.Free = -1
			} else {
				pq.Total += q.Total
				pq.Free += q.Free
			}
		}
	}

	// Convert map to slice
	var result []*model.ProviderQuota
	for _, p := range []model.Provider{model.ProviderGoogle, model.ProviderMicrosoft, model.ProviderTelegram} {
		if q, ok := quotas[p]; ok {
			result = append(result, q)
		}
	}

	return result, nil
}

// checkSoftDeletedConsistency ensures that if a file is in soft-deleted in one provider, it moves it there for others.
func (r *Runner) checkSoftDeletedConsistency(filesByPath map[string]map[model.Provider]*model.File, softDeletedPath string) error {
	// Map CalculatedID -> SoftDeletedFile (if exists)
	softDeletedIDs := make(map[string]*model.File)

	for path, fileMap := range filesByPath {
		if strings.Contains(path, softDeletedPath) {
			// Find a representative file
			for _, f := range fileMap {
				softDeletedIDs[f.CalculatedID] = f
				break
			}
		}
	}

	for path, fileMap := range filesByPath {
		// Skip if already in soft-deleted path
		if strings.Contains(path, softDeletedPath) {
			continue
		}

		for provider, file := range fileMap {
			if target, ok := softDeletedIDs[file.CalculatedID]; ok {
				// Found a file that should be soft deleted
				logger.Info("File %s (CalculatedID: %s) found in %s but soft-deleted in another provider. Moving to soft-deleted.", path, file.CalculatedID, provider)

				// Calculate target path
				// "sync-cloud-drives-aux/soft-deleted" is relative to sync root.
				// target.Path includes the full path relative to sync root.
				// We want to move 'file' to 'target.Path' (or construct if target is just representative)
				// Ideally we move it to the path defined by the file in soft-deleted.

				targetSoftPath := target.Path
				targetFolder := filepath.Dir(targetSoftPath)

				if !r.safeMode {
					// Find correct user/client
					var client api.CloudClient
					var err error

					for i := range r.config.Users {
						if r.config.Users[i].Provider == provider && (r.config.Users[i].Email == file.UserEmail || r.config.Users[i].Phone == file.UserPhone) {
							client, err = r.GetOrCreateClient(&r.config.Users[i])
							break
						}
					}

					if client == nil || err != nil {
						logger.Error("Could not find client for file %s", file.Path)
						continue
					}

					// Get target folder ID
					targetFolderID, err := r.ensureFolderStructure(client, targetFolder, provider)
					if err != nil {
						logger.Error("Failed to ensure folder structure for %s: %v", targetFolder, err)
						continue
					}

					if err := client.MoveFile(file.ID, targetFolderID); err != nil {
						logger.Error("Failed to move file %s to soft-deleted: %v", path, err)
					}
				} else {
					logger.DryRun("Would move %s to %s in %s", path, targetSoftPath, provider)
				}
			}
		}
	}
	return nil
}
