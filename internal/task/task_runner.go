package task

import (
	"fmt"
	"sort"

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

	for i := range r.config.Users {
		user := &r.config.Users[i]
		client, err := r.GetOrCreateClient(user)
		if err != nil {
			logger.ErrorTagged([]string{string(user.Provider)}, "Failed to create client: %v", err)
			continue
		}

		// Get sync folder ID
		syncFolderID, err := client.GetSyncFolderID()
		if err != nil {
			logger.ErrorTagged([]string{string(user.Provider), user.Email + user.Phone}, "Failed to get sync folder: %v", err)
			continue
		}

		if syncFolderID == "" {
			logger.InfoTagged([]string{string(user.Provider), user.Email + user.Phone}, "No sync folder, skipping")
			continue
		}

		// Scan files
		if err := r.scanFolder(client, user, syncFolderID, ""); err != nil {
			logger.ErrorTagged([]string{string(user.Provider), user.Email + user.Phone}, "Failed to scan folder: %v", err)
		}
	}

	logger.Info("Metadata gathering complete")
	return nil
}

func (r *Runner) scanFolder(client api.CloudClient, user *model.User, folderID, pathPrefix string) error {
	// List and store files
	files, err := client.ListFiles(folderID)
	if err != nil {
		return err
	}

	for _, file := range files {
		file.Path = pathPrefix + "/" + file.Name
		if err := r.db.InsertFile(file); err != nil {
			logger.WarningTagged([]string{string(user.Provider), user.Email + user.Phone}, "Failed to insert file %s: %v", file.Name, err)
		}
		
		// Insert fragments if any
		for _, fragment := range file.Fragments {
			if err := r.db.InsertFileFragment(fragment); err != nil {
				logger.WarningTagged([]string{string(user.Provider), user.Email + user.Phone}, "Failed to insert fragment for file %s: %v", file.Name, err)
			}
		}
	}

	logger.InfoTagged([]string{string(user.Provider), user.Email + user.Phone}, "Found %d files in folder %s", len(files), folderID)

	// Recursively scan subfolders
	folders, err := client.ListFolders(folderID)
	if err != nil {
		return err
	}

	for _, folder := range folders {
		folder.Path = pathPrefix + "/" + folder.Name
		if err := r.db.InsertFolder(folder); err != nil {
			logger.WarningTagged([]string{string(user.Provider), user.Email + user.Phone}, "Failed to insert folder %s: %v", folder.Name, err)
		}

		// Recurse
		if err := r.scanFolder(client, user, folder.ID, folder.Path); err != nil {
			logger.WarningTagged([]string{string(user.Provider), user.Email + user.Phone}, "Failed to scan subfolder %s: %v", folder.Name, err)
		}
	}

	return nil
}

// CheckForDuplicates finds duplicate files within each provider
func (r *Runner) CheckForDuplicates() error {
	logger.Info("Checking for duplicate files...")

	providers := []model.Provider{model.ProviderGoogle, model.ProviderMicrosoft, model.ProviderTelegram}

	foundDuplicates := false

	for _, provider := range providers {
		ids, err := r.db.GetDuplicateCalculatedIDs(provider)
		if err != nil {
			logger.ErrorTagged([]string{string(provider)}, "Failed to query duplicates: %v", err)
			continue
		}

		if len(ids) == 0 {
			logger.InfoTagged([]string{string(provider)}, "No duplicates found")
			continue
		}

		foundDuplicates = true
		logger.InfoTagged([]string{string(provider)}, "Found %d duplicate file groups", len(ids))

		for _, id := range ids {
			files, err := r.db.GetFilesByCalculatedID(id, provider)
			if err != nil {
				continue
			}

			fmt.Printf("\n[%s] Duplicate files (CalculatedID: %s):\n", provider, id)
			for i, file := range files {
				fmt.Printf("  %d. %s (ID: %s, Size: %d, Created: %s)\n",
					i+1, file.Path, file.ID, file.Size, file.CreatedTime.Format("2006-01-02"))
			}
		}
	}

	if !foundDuplicates {
		logger.Info("No duplicates found across all providers")
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

	for path, fileMap := range filesByPath {
		// Determine master file (prioritize Google, then Microsoft, then Telegram)
		var masterFile *model.File

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

		for _, provider := range providers {
			if _, exists := fileMap[provider]; !exists {
				// File missing in this provider
				logger.Info("File %s missing in %s", path, provider)

				if !r.safeMode {
					// Logic to copy file would go here:
					// 1. Get source client
					// 2. Get destination client (find account with space)
					// 3. Download from source
					// 4. Upload to destination
					logger.Info("Copying %s from %s to %s (Not fully implemented)", path, masterFile.Provider, provider)
				} else {
					logger.DryRun("Would copy %s from %s to %s", path, masterFile.Provider, provider)
				}
			} else {
				// File exists, check calculated ID for conflict
				existingFile := fileMap[provider]
				if existingFile.CalculatedID != masterFile.CalculatedID {
					logger.Warning("Conflict detected for %s in %s (CalculatedID mismatch)", path, provider)
					// Logic to rename and upload would go here
				}
			}
		}
	}

	return nil
}
