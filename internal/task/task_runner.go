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
	config      *model.Config
	db          *database.DB
	safeMode    bool
	stopOnError bool
	clients     map[string]api.CloudClient
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

func (r *Runner) SetStopOnError(stop bool) {
	r.stopOnError = stop
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
	startTime := time.Now()

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

	// Post-processing: Link replicas to files
	logger.Info("Updating logical files from latest replicas...")
	if err := r.db.UpdateLogicalFilesFromReplicas(); err != nil {
		return fmt.Errorf("failed to update logical files: %w", err)
	}

	logger.Info("Linking replicas to logical files...")
	if err := r.db.LinkOrphanedReplicas(); err != nil {
		return fmt.Errorf("failed to link orphaned replicas: %w", err)
	}

	logger.Info("Creating new logical files for unmatched replicas...")
	if err := r.db.PromoteOrphanedReplicasToFiles(); err != nil {
		return fmt.Errorf("failed to promote orphaned replicas: %w", err)
	}

	logger.Info("Marking missing replicas as deleted...")
	if err := r.db.MarkDeletedReplicas(startTime); err != nil {
		return fmt.Errorf("failed to mark deleted replicas: %w", err)
	}

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
		if file.Name == "metadata.db" {
			continue
		}
		file.Path = pathPrefix + "/" + file.Name
		for _, replica := range file.Replicas {
			replica.Path = file.Path
		}
		fileChan <- file
	}

	logger.InfoTagged([]string{string(user.Provider), user.Email + user.Phone}, "Found %d files in folder %s", len(files), folderID)

	// Recursively scan subfolders
	folders, err := client.ListFolders(folderID)
	if err != nil {
		return err
	}

	for _, folder := range folders {
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
				// Check if any replica belongs to the source user's account
				for _, replica := range f.Replicas {
					if replica.AccountID == source.User.Email || replica.AccountID == source.User.Phone {
						candidates = append(candidates, f)
						break
					}
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

					// Update database to reflect ownership change
					if err := r.db.UpdateReplicaOwner(string(provider), source.User.Email, file.ID, target.User.Email); err != nil {
						logger.Warning("Failed to update local DB for %s: %v", file.Name, err)
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
		// Check if any replica belongs to the main user
		for _, replica := range f.Replicas {
			if replica.AccountID == mainUser.Email {
				candidates = append(candidates, f)
				break
			}
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

			// Handle pending transfer flow
			if err == api.ErrOwnershipTransferPending {
				logger.InfoTagged([]string{"Google", mainUser.Email}, "Ownership transfer pending, accepting as %s...", target.User.Email)
				if acceptErr := target.Client.AcceptOwnership(file.ID); acceptErr != nil {
					logger.Error("Failed to accept ownership: %v", acceptErr)
					continue
				}
				err = nil // Clear error as acceptance succeeded
			}

			if err != nil {
				logger.Error("Failed to transfer ownership: %v", err)
				continue
			}

			// Update database to reflect ownership change
			if err := r.db.UpdateReplicaOwner(string(model.ProviderGoogle), mainUser.Email, file.ID, target.User.Email); err != nil {
				logger.Warning("Failed to update local DB for %s: %v", file.Name, err)
				// Don't stop process, but log warning
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
	if len(files) > 0 {
		names := make([]string, 0, len(files))
		for _, f := range files {
			names = append(names, f.Name)
		}
		logger.Info("Found files in folder %s: %s", folderID, strings.Join(names, ", "))
	}

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

	// Group by normalized path - now tracking which providers have replicas for each file
	filesByPath := make(map[string]map[model.Provider]*model.File)
	for _, f := range files {
		if f.Status != "active" {
			continue
		}

		if _, ok := filesByPath[f.Path]; !ok {
			filesByPath[f.Path] = make(map[model.Provider]*model.File)
		}
		// Add entry for each provider that has a replica of this file
		for _, replica := range f.Replicas {
			if replica.Status != "active" {
				continue
			}
			filesByPath[f.Path][replica.Provider] = f
		}
	}

	// Check for missing files
	providers := []model.Provider{model.ProviderGoogle, model.ProviderMicrosoft, model.ProviderTelegram}
	softDeletedPath := "sync-cloud-drives-aux/soft-deleted"

	// Index soft deleted files by CalculatedID to detect moves to trash
	softDeletedHashes := make(map[string]bool)
	checkedAccounts := make(map[string]bool)

	for _, user := range r.config.Users {
		accountKey := string(user.Provider) + ":" + user.Email
		if checkedAccounts[accountKey] {
			continue
		}
		checkedAccounts[accountKey] = true

		if user.Provider == model.ProviderTelegram {
			continue
		}

		client, err := r.GetOrCreateClient(&user)
		if err != nil {
			logger.Warning("Failed to get client for %s: %v", user.Email, err)
			continue
		}

		// Find aux folder (syncFolderID -> sync-cloud-drives-aux)
		syncFolderID, err := client.GetSyncFolderID()
		if err != nil {
			logger.Warning("Failed to get sync folder for %s: %v", user.Email, err)
			continue
		}

		findFolder := func(parentID, name string) (string, error) {
			folders, err := client.ListFolders(parentID)
			if err != nil {
				return "", err
			}
			for _, f := range folders {
				if f.Name == name {
					return f.ID, nil
				}
			}
			return "", fmt.Errorf("not found")
		}

		auxID, err := findFolder(syncFolderID, "sync-cloud-drives-aux")
		if err != nil {
			continue
		}
		softID, err := findFolder(auxID, "soft-deleted")
		if err != nil {
			continue
		}

		files, err := client.ListFiles(softID)
		if err != nil {
			logger.Warning("Failed to list soft-deleted files for %s: %v", user.Email, err)
			continue
		}

		for _, f := range files {
			softDeletedHashes[f.CalculatedID] = true
			logger.Info("Found soft-deleted file: %s (Hash: %s) on %s", f.Name, f.CalculatedID, user.Email)
		}
	}

	for path, fileMap := range filesByPath {
		// Determine master file (prioritize Google, then Microsoft, then Telegram)
		var masterFile *model.File

		// Check if any file is in soft-deleted folder
		isSoftDeleted := false
		if strings.Contains(path, softDeletedPath) {
			isSoftDeleted = true
		}

		// Pick any file to check calculated ID
		var anyFile *model.File
		for _, f := range fileMap {
			anyFile = f
			break
		}

		// If this file matches a soft-deleted file by content, and is NOT in soft-deleted folder, move it there
		if anyFile != nil && !isSoftDeleted && softDeletedHashes[anyFile.CalculatedID] {
			logger.Info("File %s matches a soft-deleted file. Moving to soft-deleted...", path)

			for provider, f := range fileMap {
				// Find replica for this provider
				var replica *model.Replica
				for _, r := range f.Replicas {
					if r.Provider == provider {
						replica = r
						break
					}
				}
				if replica == nil {
					continue
				}

				if replica.Status != "active" {
					continue
				}

				// Get Client
				var email, phone string
				if replica.Provider == model.ProviderTelegram {
					phone = replica.AccountID
				} else {
					email = replica.AccountID
				}
				client, err := r.GetOrCreateClient(&model.User{
					Provider: replica.Provider,
					Email:    email,
					Phone:    phone,
				})
				if err != nil {
					logger.Error("Failed to get client for %s: %v", replica.AccountID, err)
					continue
				}

				// Telegram doesn't support folders, so we delete the file instead of moving to soft-deleted
				if replica.Provider == model.ProviderTelegram {
					logger.Info("Deleting soft-deleted file on Telegram: %s", f.Name)
					if err := client.DeleteFile(replica.NativeID); err != nil {
						logger.Error("Failed to delete file on Telegram: %v", err)
					} else {
						// Update replica to reflect deletion
						replica.Status = "deleted"
						// We keep the path as is for history, or clear it?
						// "deleted" status is enough to treat it as gone.
						if err := r.db.UpdateReplica(replica); err != nil {
							logger.Warning("Failed to update replica status in DB: %v", err)
						}
						// Note: We do NOT update the logical file path here because the file is gone, not moved.
						// However, other replicas might be moved.
						// If logical file 'f' is updated by other providers iterations, that's fine.
					}
					continue
				}

				// Ensure soft-deleted folder exists
				destID, err := r.ensureFolderStructure(client, softDeletedPath, provider)
				if err != nil {
					logger.Error("Failed to ensure soft-deleted folder: %v", err)
					continue
				}

				// Move file
				logger.Info("Moving %s to soft-deleted on %s...", f.Name, provider)
				if err := client.MoveFile(replica.NativeID, destID); err != nil {
					logger.Error("Failed to move file to soft-deleted: %v", err)
				} else {
					// Update DB (rudimentary, ideally we re-scan or update replica path)
					// Verify logic relies on DB. We should update the replica status or path in DB.
					// For now, let's mark it as 'deleted' or update path to soft-deleted in DB?
					// Updating path to soft-deleted is better.
					// But we don't have easy DB update method here for "Update Replica Path".
					// We can assume next metadata scan fixes it. But test might verify immediately?
					// Test calls verifyGone -> GetFileByPath(originalPath).
					// If we moved it, GetFileByPath(originalPath) should assume it's gone?
					// No, DB still has old path.
					// We must update DB.

					// Let's delete the file/replica from DB to match "Soft Deleted = Gone from View".
					// Or change its path to the new path.
					newPath := "/" + softDeletedPath + "/" + f.Name
					// Update replica path
					replica.Path = newPath
					if err := r.db.UpdateReplica(replica); err != nil {
						logger.Warning("Failed to update replica path in DB: %v", err)
					}

					// Update logical file path so verifying by old path fails/returns nil
					f.Path = newPath
					if err := r.db.UpdateFile(f); err != nil {
						logger.Warning("Failed to update file path in DB: %v", err)
					}
				}
			}
			// Don't process this file further (don't sync it back)
			continue
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
						if r.stopOnError {
							return fmt.Errorf("failed to copy file %s to %s: %w", path, provider, err)
						}
					}
				} else {
					// Get the source provider from first replica
					sourceProvider := ""
					if len(masterFile.Replicas) > 0 {
						sourceProvider = string(masterFile.Replicas[0].Provider)
					}
					logger.DryRun("Would copy %s from %s to %s", path, sourceProvider, provider)
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
							if r.stopOnError {
								return fmt.Errorf("failed to resolve conflict for %s in %s: %w", path, provider, err)
							}
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
		if r.stopOnError {
			return fmt.Errorf("distribute shortcuts failed: %w", err)
		}
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
			// Check if file has a Microsoft replica
			for _, replica := range f.Replicas {
				if replica.Provider == model.ProviderMicrosoft {
					msFiles = append(msFiles, f)
					break
				}
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
				// Check if this file has a replica for this user
				for _, replica := range f.Replicas {
					if replica.Provider == model.ProviderMicrosoft && replica.AccountID == user.Email {
						hasIt = true
						break
					}
				}
				if hasIt {
					break
				}
			}

			if !hasIt {
				if !r.safeMode {
					if err := r.createShortcut(sourceFile, &user); err != nil {
						logger.Error("Failed to create shortcut for %s in %s: %v", path, user.Email, err)
						if r.stopOnError {
							return fmt.Errorf("failed to create shortcut for %s: %w", path, err)
						}
					}
				} else {
					// Get source account from first replica
					sourceAccount := ""
					if len(sourceFile.Replicas) > 0 {
						sourceAccount = sourceFile.Replicas[0].AccountID
					}
					logger.DryRun("Would create shortcut for %s in %s -> %s", path, user.Email, sourceAccount)
				}
			}
		}
	}

	// Distribute Folders (for empty folders)
	logger.Info("Syncing folder structures...")
	allFolders, err := r.db.GetAllFolders()
	if err == nil {
		paths := make([]string, 0)
		seen := make(map[string]bool)
		for _, f := range allFolders {
			if f.Name == "metadata.db" || f.Name == "sync-cloud-drives-aux" || f.Name == "soft-deleted" || strings.Contains(f.Path, "sync-cloud-drives-aux") {
				continue
			}
			if !seen[f.Path] {
				paths = append(paths, f.Path)
				seen[f.Path] = true
			}
		}

		for i := range r.config.Users {
			u := &r.config.Users[i]
			if u.Provider == model.ProviderTelegram {
				continue
			}

			logger.InfoTagged([]string{string(u.Provider), u.Email}, "Verifying folder structure...")
			client, err := r.GetOrCreateClient(u)
			if err != nil {
				continue
			}
			rootID, err := client.GetSyncFolderID()
			if err != nil {
				continue
			}

			// Cache for this user: parentID -> map[name]id
			cache := make(map[string]map[string]string)

			var getFolderID func(string, string) (string, error)
			getFolderID = func(parentID, name string) (string, error) {
				if m, ok := cache[parentID]; ok {
					if id, ok := m[name]; ok {
						return id, nil
					}
				}

				// List
				items, err := client.ListFolders(parentID)
				if err != nil {
					return "", err
				}

				m := make(map[string]string)
				for _, item := range items {
					m[item.Name] = item.ID
				}
				cache[parentID] = m

				if id, ok := m[name]; ok {
					return id, nil
				}
				return "", nil // Not found
			}

			for _, path := range paths {
				relPath := strings.TrimPrefix(path, "/")
				if relPath == "" {
					continue
				}
				parts := strings.Split(relPath, "/")

				currentID := rootID
				for _, part := range parts {
					if part == "" {
						continue
					}

					id, err := getFolderID(currentID, part)
					if err != nil {
						break
					} // Error listing

					if id == "" {
						// Create
						if !r.safeMode {
							newF, err := client.CreateFolder(currentID, part)
							if err != nil {
								logger.Warning("Failed to create folder %s: %v", part, err)
								break
							}
							id = newF.ID
							// Update cache
							if cache[currentID] == nil {
								cache[currentID] = make(map[string]string)
							}
							cache[currentID][part] = id
						} else {
							logger.DryRun("Would create folder %s in %s", part, currentID)
							break // Can't proceed safely
						}
					}
					currentID = id
				}
			}
		}
	} else {
		logger.Error("Failed to get folders from DB: %v", err)
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

	// Populate SyncFolderUsed from DB
	for p := range quotas {
		usage, err := r.db.GetProviderUsage(p)
		if err != nil {
			logger.Warning("Failed to calculate used storage in sync folder for %s: %v", p, err)
		} else {
			quotas[p].SyncFolderUsed = usage
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
					// Find correct user/client using replicas
					var client api.CloudClient
					var err error

					// Find the replica for this provider
					var targetReplica *model.Replica
					for _, replica := range file.Replicas {
						if replica.Provider == provider {
							targetReplica = replica
							break
						}
					}

					if targetReplica == nil {
						logger.Error("Could not find replica for provider %s for file %s", provider, file.Path)
						continue
					}

					// Find the user for this replica
					for i := range r.config.Users {
						if r.config.Users[i].Provider == provider && (r.config.Users[i].Email == targetReplica.AccountID || r.config.Users[i].Phone == targetReplica.AccountID) {
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
