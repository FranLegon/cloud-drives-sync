package task

import (
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/FranLegon/cloud-drives-sync/internal/api"
	"github.com/FranLegon/cloud-drives-sync/internal/auth"
	"github.com/FranLegon/cloud-drives-sync/internal/config"
	"github.com/FranLegon/cloud-drives-sync/internal/database"
	"github.com/FranLegon/cloud-drives-sync/internal/google"
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/FranLegon/cloud-drives-sync/internal/microsoft"
	"github.com/FranLegon/cloud-drives-sync/internal/model"
	"github.com/FranLegon/cloud-drives-sync/internal/telegram"
)

// backupStatus tracks the state of a backup account during FreeMain
type backupStatus struct {
	User   model.User
	Client api.CloudClient
	Quota  *api.QuotaInfo
	Free   int64
}

// accountQuota caches the quota for an account to avoid repeated API calls
type accountQuota struct {
	Total int64
	Used  int64
}

// Runner handles task orchestration
type Runner struct {
	config                *model.Config
	db                    *database.DB
	safeMode              bool
	stopOnError           bool
	clients               map[string]api.CloudClient
	clientsMu             sync.RWMutex
	folderLocks           sync.Map        // protects ensureFolderStructure per account from concurrent creates
	msShareFailureCache   map[string]bool // Cache of failed Microsoft sharing attempts (sourceAccount:targetAccount)
	msShareFailureCacheMu sync.RWMutex
	accountQuotas         map[string]*accountQuota
	accountQuotasMu       sync.Mutex
	folderCache           sync.Map        // Cache of resolved folder IDs (path+account -> ID)
}

// NewRunner creates a new task runner
func NewRunner(config *model.Config, db *database.DB, safeMode bool) *Runner {
	return &Runner{
		config:              config,
		db:                  db,
		safeMode:            safeMode,
		clients:             make(map[string]api.CloudClient),
		msShareFailureCache: make(map[string]bool),
		accountQuotas:       make(map[string]*accountQuota),
	}
}

func (r *Runner) SetStopOnError(stop bool) {
	r.stopOnError = stop
}

// getAccountFolderLock returns a mutex for the given provider and account to serialize folder creation
func (r *Runner) getAccountFolderLock(provider model.Provider, accountID string) *sync.Mutex {
	key := string(provider) + ":" + accountID
	if v, ok := r.folderLocks.Load(key); ok {
		return v.(*sync.Mutex)
	}
	v, _ := r.folderLocks.LoadOrStore(key, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// GetOrCreateClient gets or creates a client for a user
func (r *Runner) GetOrCreateClient(user *model.User) (api.CloudClient, error) {
	key := string(user.Provider) + ":" + user.Email + user.Phone

	r.clientsMu.RLock()
	if client, exists := r.clients[key]; exists {
		r.clientsMu.RUnlock()
		return client, nil
	}
	r.clientsMu.RUnlock()

	r.clientsMu.Lock()
	defer r.clientsMu.Unlock()

	// Double-check after acquiring write lock
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

			apiSem := make(chan struct{}, 8) // Limit concurrent API calls per account

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
			if err := r.scanFolder(client, user, syncFolderID, "", fileChan, folderChan, apiSem); err != nil {
				logger.ErrorTagged([]string{string(user.Provider), user.Email + user.Phone}, "Failed to scan folder: %v", err)
			}
		}(&r.config.Users[i])
	}

	wg.Wait()
	close(fileChan)
	close(folderChan)
	dbWg.Wait()

	// Post-processing: Link replicas to files
	logger.Info("Linking replicas to logical files...")
	if err := r.db.LinkOrphanedReplicas(); err != nil {
		return fmt.Errorf("failed to link orphaned replicas: %w", err)
	}

	logger.Info("Updating logical files from latest replicas...")
	if err := r.db.UpdateLogicalFilesFromReplicas(); err != nil {
		return fmt.Errorf("failed to update logical files: %w", err)
	}

	logger.Info("Creating new logical files for unmatched replicas...")
	if err := r.db.PromoteOrphanedReplicasToFiles(); err != nil {
		return fmt.Errorf("failed to promote orphaned replicas: %w", err)
	}

	logger.Info("Updating soft-deleted file status...")
	if err := r.db.UpdateSoftDeletedFileStatus(startTime); err != nil {
		return fmt.Errorf("failed to update soft-deleted status: %w", err)
	}

	logger.Info("Marking missing replicas as deleted...")
	if err := r.db.MarkDeletedReplicas(startTime); err != nil {
		return fmt.Errorf("failed to mark deleted replicas: %w", err)
	}

	logger.Info("Processing hard deletes...")
	if err := r.ProcessHardDeletes(); err != nil {
		logger.Error("Failed to process hard deletes: %v", err)
	}

	logger.Info("Metadata gathering complete")
	return nil
}

func (r *Runner) dbWriter(fileChan <-chan *model.File, folderChan <-chan *model.Folder) {
	const batchSize = 500
	const flushInterval = 2 * time.Second

	fileBuffer := make([]*model.File, 0, batchSize)
	folderBuffer := make([]*model.Folder, 0, batchSize)

	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	flushFiles := func() {
		if len(fileBuffer) > 0 {
			if err := r.db.BatchInsertFiles(fileBuffer); err != nil {
				logger.Error("Failed to batch insert files: %v", err)
			}
			fileBuffer = fileBuffer[:0]
		}
	}

	flushFolders := func() {
		if len(folderBuffer) > 0 {
			if err := r.db.BatchInsertFolders(folderBuffer); err != nil {
				logger.Error("Failed to batch insert folders: %v", err)
			}
			folderBuffer = folderBuffer[:0]
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

func (r *Runner) scanFolder(client api.CloudClient, user *model.User, folderID, pathPrefix string, fileChan chan<- *model.File, folderChan chan<- *model.Folder, apiSem chan struct{}) error {
	// List and store files
	apiSem <- struct{}{}
	files, err := client.ListFiles(folderID)
	<-apiSem
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
	apiSem <- struct{}{}
	folders, err := client.ListFolders(folderID)
	<-apiSem
	if err != nil {
		return err
	}

	var wg sync.WaitGroup
	errCh := make(chan error, len(folders))

	for _, folder := range folders {
		folder.Path = pathPrefix + "/" + folder.Name
		folderChan <- folder

		wg.Add(1)
		go func(f *model.Folder) {
			defer wg.Done()
			if err := r.scanFolder(client, user, f.ID, f.Path, fileChan, folderChan, apiSem); err != nil {
				errCh <- err
			}
		}(folder)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
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
	googleMain := config.GetMainAccount(r.config, model.ProviderGoogle)
	if googleMain != nil {
		client, err := r.GetOrCreateClient(googleMain)
		if err != nil {
			return err
		}

		syncFolderID, err := client.GetSyncFolderID()
		if err != nil {
			return err
		}

		backupAccounts := config.GetBackupAccounts(r.config, model.ProviderGoogle)
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

		// Pre-fetch files once for all sources
		files, err := r.db.GetAllFiles()
		if err != nil {
			logger.Error("Failed to get files from DB for balancing: %v", err)
			continue
		}

		// Process sources
		for _, source := range sources {
			logger.InfoTagged([]string{string(provider), source.User.Email}, "Account is over quota, looking for files to move...")

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

			sortFilesBySizeDesc(candidates)

			for _, file := range candidates {
				// Stop if source is safe
				if source.UsagePct < 90.0 {
					logger.InfoTagged([]string{string(provider), source.User.Email}, "Account is now under safe threshold")
					break
				}

				// Sort targets by most free space (descending) - re-sort each time as space changes
				sort.Slice(targets, func(i, j int) bool {
					freeI := targets[i].Quota.Total - targets[i].Quota.Used
					freeJ := targets[j].Quota.Total - targets[j].Quota.Used
					return freeI > freeJ
				})

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

				// Find the replica for this source account
				var sourceReplica *model.Replica
				for _, replica := range file.Replicas {
					if replica.AccountID == source.User.Email || replica.AccountID == source.User.Phone {
						sourceReplica = replica
						break
					}
				}

				if sourceReplica == nil {
					logger.Warning("Could not find replica for file %s in account %s", file.Name, source.User.Email)
					continue
				}

				// Move file (Transfer Ownership)
				if !r.safeMode {
					logger.InfoTagged([]string{string(provider), source.User.Email}, "Transferring %s (%d bytes) to %s", file.Name, file.Size, target.User.Email)
					err := source.Client.TransferOwnership(sourceReplica.NativeID, target.User.Email)
					if err != nil {
						if err == api.ErrOwnershipTransferPending {
							logger.InfoTagged([]string{string(provider), source.User.Email}, "Ownership transfer pending, accepting as %s...", target.User.Email)
							if err := target.Client.AcceptOwnership(sourceReplica.NativeID); err != nil {
								logger.Error("Failed to accept ownership: %v", err)
								continue
							}
						} else {
							logger.Error("Failed to transfer ownership: %v", err)
							continue
						}
					}

					// Update database to reflect ownership change
					if err := r.db.UpdateReplicaOwner(string(provider), source.User.Email, sourceReplica.NativeID, target.User.Email); err != nil {
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
func (r *Runner) FreeMain() (bool, error) {
	logger.Info("Freeing up main account storage...")

	filesMoved := false

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
		return filesMoved, nil
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
		return filesMoved, nil
	}

	logger.Info("Processing Google (Main: %s)", mainUser.Email)

	// Initialize main client
	mainClient, err := r.GetOrCreateClient(mainUser)
	if err != nil {
		logger.Error("Failed to create client for main account: %v", err)
		return filesMoved, err
	}

	// Get backup accounts status
	var targets []*backupStatus

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
			targets = append(targets, &backupStatus{
				User:   user,
				Client: client,
				Quota:  quota,
				Free:   free,
			})
		}
	}

	if len(targets) == 0 {
		logger.Warning("No backup accounts with free space available for Google")
		return filesMoved, nil
	}

	// List files in main account using DB
	files, err := r.db.GetAllFiles()
	if err != nil {
		logger.Error("Failed to get files from DB: %v", err)
		return filesMoved, err
	}

	// Filter files owned by main user
	var candidates []*model.File
	for _, f := range files {
		// Check if any replica is OWNED by the main user (not just present in their account)
		for _, replica := range f.Replicas {
			if replica.AccountID == mainUser.Email && replica.Owner == mainUser.Email {
				candidates = append(candidates, f)
				break
			}
		}
	}

	if len(candidates) == 0 {
		logger.Info("No files found in main account to move")
		return filesMoved, nil
	}

	// Sort files by size (descending) to move big chunks first
	sortFilesBySizeDesc(candidates)

	// Move files
	for _, file := range candidates {
		// Sort targets by free space (descending) - re-sort each time as space changes
		sort.Slice(targets, func(i, j int) bool {
			return targets[i].Free > targets[j].Free
		})

		// Find best target
		var target *backupStatus
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
			// Find the main replica for this file to get the NativeID
			var mainReplica *model.Replica
			for _, replica := range file.Replicas {
				if replica.AccountID == mainUser.Email && replica.Owner == mainUser.Email {
					mainReplica = replica
					break
				}
			}

			if mainReplica == nil {
				logger.Warning("Could not find main replica for file %s", file.Name)
				continue
			}

			if !r.safeMode {
				logger.InfoTagged([]string{"Google", mainUser.Email}, "Transferring %s (%d bytes) to %s", file.Name, file.Size, target.User.Email)
				err := mainClient.TransferOwnership(mainReplica.NativeID, target.User.Email)

				// Handle pending transfer flow
				if err == api.ErrOwnershipTransferPending {
					logger.InfoTagged([]string{"Google", mainUser.Email}, "Ownership transfer pending, accepting as %s...", target.User.Email)
					if acceptErr := target.Client.AcceptOwnership(mainReplica.NativeID); acceptErr != nil {
						logger.Error("Failed to accept ownership: %v", acceptErr)
						err = fmt.Errorf("acceptance failed: %w", acceptErr)
					} else {
						err = nil // Clear error as acceptance succeeded

						// Move file to target's sync folder (it's currently in root after pending owner flow)
						dir := filepath.Dir(file.Path)
						dir = strings.ReplaceAll(dir, "\\", "/")
						if dir == "." || dir == "" {
							dir = "/"
						}
						targetFolderID, folderErr := r.ensureFolderStructure(target.Client, dir, target.User.Provider)
						if folderErr != nil {
							logger.Warning("Failed to resolve target sync folder for %s: %v", file.Name, folderErr)
						} else {
							if mvErr := target.Client.MoveFile(mainReplica.NativeID, targetFolderID); mvErr != nil {
								logger.Warning("Failed to move transferred file %s to sync folder: %v", file.Name, mvErr)
							} else {
								logger.InfoTagged([]string{"Google", target.User.Email}, "Moved %s to sync folder", file.Name)
							}
						}
					}
				}

				fallbackUsed := false

				if err != nil {
					// Check for consent error Consumer to Consumer transfer restriction
					if strings.Contains(err.Error(), "Consent is required") || strings.Contains(err.Error(), "consentRequiredForOwnershipTransfer") || strings.Contains(err.Error(), "transferOwnership parameter must be enabled") {
						fallbackUsed = true
						if fallbackErr := r.fallbackCopyDelete(mainClient, target, file, mainReplica.NativeID, mainUser.Email, mainReplica); fallbackErr != nil {
							logger.Error("Fallback transfer failed: %v", fallbackErr)
							continue
						}
						filesMoved = true
						err = nil // Cleared
					} else {
						logger.Error("Failed to transfer ownership: %v", err)
						continue
					}
				}

				if err == nil && !fallbackUsed {
					// Update database to reflect ownership change for standard transfer
					if dbErr := r.db.UpdateReplicaOwner(string(model.ProviderGoogle), mainUser.Email, mainReplica.NativeID, target.User.Email); dbErr != nil {
						logger.Warning("Failed to update local DB for %s: %v", file.Name, dbErr)
					}
					filesMoved = true
				}
			} else {
				logger.DryRunTagged([]string{"Google", mainUser.Email}, "Would transfer %s (%d bytes) to %s", file.Name, file.Size, target.User.Email)
				filesMoved = true // simulate move in dry run
			}

		// Update local state
		if filesMoved {
			target.Free -= file.Size
			target.Quota.Used += file.Size
		}
	}

	return filesMoved, nil
}

// fallbackCopyDelete performs a download+upload+delete transfer when ownership transfer is not supported
func (r *Runner) fallbackCopyDelete(mainClient api.CloudClient, target *backupStatus, file *model.File, nativeID string, mainEmail string, oldReplicaDB *model.Replica) error {
	logger.InfoTagged([]string{"Google", mainEmail}, "Transfer via ownership not supported (consent required). Falling back to Copy+Delete...")

	// 1. Download
	pr, pw := io.Pipe()
	downloadErrChan := make(chan error, 1)

	go func() {
		defer pw.Close()
		logger.Info("Downloading %s for fallback transfer...", file.Name)
		if err := mainClient.DownloadFile(nativeID, pw); err != nil {
			downloadErrChan <- err
		} else {
			downloadErrChan <- nil
		}
		close(downloadErrChan)
	}()

	// 2. Upload - ensure destination folder exists
	dir := filepath.Dir(file.Path)
	dir = strings.ReplaceAll(dir, "\\", "/")
	if dir == "." || dir == "" {
		dir = "/"
	}

	logger.Info("Ensuring folder structure for %s on target...", dir)
	targetFolderID, folderErr := r.ensureFolderStructure(target.Client, dir, target.User.Provider)
	if folderErr != nil {
		pr.Close()
		return fmt.Errorf("failed to ensure destination folder: %w", folderErr)
	}

	logger.Info("Uploading %s to target...", file.Name)
	uploadedFile, uploadErr := target.Client.UploadFile(targetFolderID, file.Name, pr, file.Size)

	if dlErr := <-downloadErrChan; dlErr != nil {
		return fmt.Errorf("download failed: %w", dlErr)
	}
	if uploadErr != nil {
		return fmt.Errorf("upload failed: %w", uploadErr)
	}

	logger.InfoTagged([]string{"Google", mainEmail}, "Fallback: Copy successful, deleting original...")

	// 3. Delete original file
	deleteSucceeded := true
	if delErr := mainClient.DeleteFile(nativeID); delErr != nil {
		deleteSucceeded = false
		logger.Error("Fallback: Failed to delete original file: %v", delErr)
	}

	// 4. Update DB
	r.reconcileFallbackDB(uploadedFile, oldReplicaDB, target, file, mainEmail, deleteSucceeded)

	return nil
}

// reconcileFallbackDB updates the database after a fallback copy+delete transfer
func (r *Runner) reconcileFallbackDB(uploadedFile *model.File, oldReplicaDB *model.Replica, target *backupStatus, file *model.File, mainEmail string, deleteSucceeded bool) {
	if uploadedFile == nil || len(uploadedFile.Replicas) == 0 {
		return
	}

	newReplica := uploadedFile.Replicas[0]

	if oldReplicaDB == nil {
		logger.Warning("Could not find original replica in DB for fallback update: %s", file.Name)
		logger.Info("Database out of sync - metadata refresh will reconcile this on next sync")
		if !deleteSucceeded {
			logger.Warning("Original file could not be deleted - duplicate will exist until manual cleanup")
		}
		return
	}

	if deleteSucceeded {
		oldNativeID := oldReplicaDB.NativeID
		oldReplicaDB.NativeID = newReplica.NativeID
		oldReplicaDB.AccountID = target.User.Email
		oldReplicaDB.Owner = target.User.Email
		oldReplicaDB.ModTime = time.Now()

		logger.Info("Updating replica DB: OldID=%s, NewID=%s, NewOwner=%s", file.ID, newReplica.NativeID, target.User.Email)
		if dbErr := r.db.UpdateReplica(oldReplicaDB); dbErr != nil {
			logger.Warning("Failed to update replica details in DB: %v", dbErr)
			return
		}

		logger.Info("Cleaning stale replicas with OldID=%s...", oldNativeID)
		if dbErr := r.db.DeleteStaleReplicasByNativeID(model.ProviderGoogle, oldNativeID, oldReplicaDB.ID); dbErr != nil {
			logger.Warning("Failed to clean stale replicas: %v", dbErr)
		}

		// Delete the file from all other shared backup accounts
		logger.Info("Deleting stale shared copies from other backup accounts...")
		for i := range r.config.Users {
			backupUser := &r.config.Users[i]
			if backupUser.Provider != model.ProviderGoogle || backupUser.Email == target.User.Email || backupUser.Email == mainEmail {
				continue
			}
			backupClient, clientErr := r.GetOrCreateClient(backupUser)
			if clientErr != nil {
				logger.Warning("[%s] Failed to get client for cleanup: %v", backupUser.Email, clientErr)
				continue
			}
			if delErr := backupClient.DeleteFile(oldNativeID); delErr != nil {
				logger.Info("[%s] Cleanup: file %s already removed or not accessible", backupUser.Email, oldNativeID)
			} else {
				logger.Info("[%s] Deleted stale shared copy %s", backupUser.Email, file.Name)
			}
		}
	} else {
		// Delete failed, record copied target as additional active replica
		newReplicaDB := *oldReplicaDB
		newReplicaDB.ID = 0
		newReplicaDB.NativeID = newReplica.NativeID
		newReplicaDB.AccountID = target.User.Email
		newReplicaDB.Owner = target.User.Email
		newReplicaDB.ModTime = time.Now()

		if dbErr := r.db.InsertReplica(&newReplicaDB); dbErr != nil {
			logger.Warning("Failed to insert fallback copied replica in DB: %v", dbErr)
		}
	}
}

// SyncProviders synchronizes files across all providers
func (r *Runner) SyncProviders() error {
	logger.Info("Synchronizing providers...")

	// Get all files
	files, err := r.db.GetAllFilesAcrossProviders()
	if err != nil {
		return fmt.Errorf("failed to get files: %w", err)
	}

	softDeletedPath := AuxFolder + "/" + SoftDeletedFolder

	// Phase 1: Converge soft-deleted status across providers
	if err := r.convergeReplicaStatus(files, softDeletedPath); err != nil {
		return fmt.Errorf("status convergence failed: %w", err)
	}

	// Phase 2: Copy missing files and resolve conflicts
	filesByPath := buildFilesByPath(files)
	if err := r.syncMissingAndConflicts(filesByPath, softDeletedPath); err != nil {
		return err
	}

	// Check for soft-delete consistency
	if err := r.checkSoftDeletedConsistency(filesByPath, softDeletedPath); err != nil {
		logger.Error("Failed to check soft deleted consistency: %v", err)
	}

	// Phase 3: Distribute Shortcuts for Microsoft OneDrive
	if err := r.distributeShortcuts(files); err != nil {
		logger.Error("Failed to distribute shortcuts: %v", err)
		if r.stopOnError {
			return fmt.Errorf("distribute shortcuts failed: %w", err)
		}
	}

	return nil
}

// buildFilesByPath groups active files by path and provider
func buildFilesByPath(all []*model.File) map[string]map[model.Provider]*model.File {
	result := make(map[string]map[model.Provider]*model.File, len(all))
	for _, f := range all {
		if f.Status != "active" {
			continue
		}
		if _, ok := result[f.Path]; !ok {
			result[f.Path] = make(map[model.Provider]*model.File)
		}
		for _, replica := range f.Replicas {
			if replica.Status != "active" {
				continue
			}
			result[f.Path][replica.Provider] = f
		}
	}
	return result
}

// buildMainAccountSet returns a map of provider -> set of main account IDs
func (r *Runner) buildMainAccountSet() map[model.Provider]map[string]bool {
	mainAccounts := make(map[model.Provider]map[string]bool)
	for _, u := range r.config.Users {
		if !u.IsMain {
			continue
		}
		if _, ok := mainAccounts[u.Provider]; !ok {
			mainAccounts[u.Provider] = make(map[string]bool)
		}
		if u.Provider == model.ProviderTelegram {
			mainAccounts[u.Provider][u.Phone] = true
		} else {
			mainAccounts[u.Provider][u.Email] = true
		}
	}
	return mainAccounts
}

// isMainReplica checks if a replica belongs to a main account
func isMainReplica(replica *model.Replica, mainAccounts map[model.Provider]map[string]bool) bool {
	accounts, ok := mainAccounts[replica.Provider]
	if !ok {
		return false
	}
	return accounts[replica.AccountID]
}

// statusIntent tracks the intended status for a file across providers
type statusIntent struct {
	Status     string
	ActivePath string
	SoftPath   string
}

// convergeReplicaStatus ensures replicas are moved to/from soft-deleted based on main account status
func (r *Runner) convergeReplicaStatus(files []*model.File, softDeletedPath string) error {
	mainAccounts := r.buildMainAccountSet()

	filesByCalculatedID := make(map[string][]*model.File, len(files))
	for _, f := range files {
		if f.Status != "active" || f.CalculatedID == "" {
			continue
		}
		filesByCalculatedID[f.CalculatedID] = append(filesByCalculatedID[f.CalculatedID], f)
	}

	intents := make(map[string]statusIntent, len(filesByCalculatedID))
	for _, f := range files {
		if f.Status != "active" || f.CalculatedID == "" {
			continue
		}

		for _, replica := range f.Replicas {
			if replica.Status != "active" {
				continue
			}

			intent := intents[f.CalculatedID]
			inSoftDeleted := strings.Contains(strings.ReplaceAll(f.Path, "\\", "/"), softDeletedPath)

			if inSoftDeleted {
				intent.Status = "soft-deleted"
				intent.SoftPath = f.Path
			} else {
				if isMainReplica(replica, mainAccounts) && intent.Status != "soft-deleted" {
					intent.Status = "active"
					intent.ActivePath = f.Path
				}
			}

			intents[f.CalculatedID] = intent
		}
	}

	var wg sync.WaitGroup
	// Use bounded concurrency to avoid flooding network
	sem := make(chan struct{}, 4)

	for calculatedID, intent := range intents {
		fileSet := filesByCalculatedID[calculatedID]
		for _, f := range fileSet {
			for _, replica := range f.Replicas {
				if replica.Status != "active" || isMainReplica(replica, mainAccounts) {
					continue
				}

				replicaInSoftDeleted := strings.Contains(strings.ReplaceAll(replica.Path, "\\", "/"), softDeletedPath)

				switch intent.Status {
				case "soft-deleted":
					if replicaInSoftDeleted {
						continue
					}
					wg.Add(1)
					sem <- struct{}{}
					go func(rep *model.Replica, fname string) {
						defer wg.Done()
						defer func() { <-sem }()
						r.softDeleteReplica(rep, fname, softDeletedPath)
					}(replica, f.Name)

				case "active":
					if !replicaInSoftDeleted || intent.ActivePath == "" || replica.Provider == model.ProviderTelegram {
						continue
					}
					wg.Add(1)
					sem <- struct{}{}
					go func(rep *model.Replica, fname, apath string) {
						defer wg.Done()
						defer func() { <-sem }()
						r.moveReplicaToPath(rep, fname, apath)
					}(replica, f.Name, intent.ActivePath)
				}
			}
		}
	}
	wg.Wait()

	return nil
}

// softDeleteReplica moves a replica to the soft-deleted folder, or marks it as deleted for Telegram
func (r *Runner) softDeleteReplica(replica *model.Replica, fileName, softDeletedPath string) {
	if replica.Provider == model.ProviderTelegram {
		r.markTelegramReplicaDeleted(replica, fileName)
		return
	}
	r.moveReplicaToPath(replica, fileName, "/"+softDeletedPath+"/"+fileName)
}

// markTelegramReplicaDeleted marks a Telegram replica as deleted
func (r *Runner) markTelegramReplicaDeleted(replica *model.Replica, fileName string) {
	user := r.getUser(replica.Provider, replica.AccountID)
	if user == nil {
		logger.Error("User not found for telegram replica %s", replica.AccountID)
		return
	}
	client, err := r.GetOrCreateClient(user)
	if err != nil {
		logger.Error("Failed to get telegram client for %s: %v", replica.AccountID, err)
		return
	}
	if tgClient, ok := client.(*telegram.Client); ok {
		if r.safeMode {
			logger.DryRun("Would mark soft-deleted file on Telegram: %s", fileName)
			return
		}
		logger.Info("Marking soft-deleted file on Telegram: %s", fileName)
		if err := tgClient.UpdateFileStatus(replica, "deleted"); err != nil {
			logger.Error("Failed to update file status on Telegram: %v", err)
			return
		}
		replica.Status = "deleted"
		if err := r.db.UpdateReplica(replica); err != nil {
			logger.Warning("Failed to update telegram replica status in DB: %v", err)
		}
	}
}

// moveReplicaToPath moves a replica to the given target path
func (r *Runner) moveReplicaToPath(replica *model.Replica, fileName, targetPath string) {
	user := r.getUser(replica.Provider, replica.AccountID)
	if user == nil {
		logger.Error("User not found for replica %s on %s", replica.AccountID, replica.Provider)
		return
	}

	client, err := r.GetOrCreateClient(user)
	if err != nil {
		logger.Error("Failed to get client for %s: %v", replica.AccountID, err)
		return
	}

	targetDir := filepath.Dir(targetPath)
	targetDir = strings.ReplaceAll(targetDir, "\\", "/")
	if targetDir == "." || targetDir == "" {
		targetDir = "/"
	}

	destID, err := r.ensureFolderStructure(client, targetDir, replica.Provider)
	if err != nil {
		logger.Error("Failed to ensure folder structure for %s: %v", targetDir, err)
		return
	}

	if r.safeMode {
		logger.DryRun("Would move %s on %s to %s", fileName, replica.Provider, targetPath)
		return
	}

	if err := client.MoveFile(replica.NativeID, destID); err != nil {
		logger.Error("Failed to move %s on %s to %s: %v", fileName, replica.Provider, targetPath, err)
		return
	}

	replica.Path = strings.ReplaceAll(filepath.Join(targetDir, fileName), "\\", "/")
	if err := r.db.UpdateReplica(replica); err != nil {
		logger.Warning("Failed to update replica path in DB: %v", err)
	}
}

// copyJob represents a file copy operation to be executed by a worker
type copyJob struct {
	masterFile *model.File
	provider   model.Provider
	targetName string // empty for normal copy, non-empty for conflict resolution
	path       string // for error reporting
}

// getMasterFile resolves the primary file from a file map using priority logic
func getMasterFile(fileMap map[model.Provider]*model.File) *model.File {
	if f, ok := fileMap[model.ProviderGoogle]; ok {
		return f
	} else if f, ok := fileMap[model.ProviderMicrosoft]; ok {
		return f
	} else if f, ok := fileMap[model.ProviderTelegram]; ok {
		return f
	}
	return nil
}

// sortFilesBySizeDesc sorts a slice of files by size in descending order
func sortFilesBySizeDesc(files []*model.File) {
	sort.Slice(files, func(i, j int) bool {
		return files[i].Size > files[j].Size
	})
}

// syncMissingAndConflicts copies missing files across providers, resolves conflicts, and enforces soft-delete placement
func (r *Runner) syncMissingAndConflicts(filesByPath map[string]map[model.Provider]*model.File, softDeletedPath string) error {
	providers := []model.Provider{model.ProviderGoogle, model.ProviderMicrosoft, model.ProviderTelegram}

	// Phase 1: Handle soft-deleted placements (sequential, fast)
	for path, fileMap := range filesByPath {
		if !strings.Contains(path, softDeletedPath) {
			continue
		}
		
		if masterFile := getMasterFile(fileMap); masterFile != nil {
			r.enforceSoftDeletedPlacement(masterFile, softDeletedPath)
		}
	}

	// Phase 2: Collect copy jobs for missing files and conflicts
	var jobs []copyJob

	for path, fileMap := range filesByPath {
		if strings.Contains(path, softDeletedPath) {
			continue
		}

		masterFile := getMasterFile(fileMap)
		if masterFile == nil {
			continue
		}

		for _, provider := range providers {
			if _, exists := fileMap[provider]; !exists {
				logger.Info("File %s missing in %s", path, provider)

				if r.safeMode {
					sourceProvider := ""
					if len(masterFile.Replicas) > 0 {
						sourceProvider = string(masterFile.Replicas[0].Provider)
					}
					logger.DryRun("Would copy %s from %s to %s", path, sourceProvider, provider)
				} else {
					jobs = append(jobs, copyJob{
						masterFile: masterFile,
						provider:   provider,
						targetName: "",
						path:       path,
					})
				}
			} else {
				existingFile := fileMap[provider]
				if existingFile.CalculatedID != masterFile.CalculatedID {
					logger.Warning("Conflict detected for %s in %s (CalculatedID mismatch)", path, provider)

					ext := filepath.Ext(masterFile.Name)
					nameWithoutExt := strings.TrimSuffix(masterFile.Name, ext)
					timestamp := time.Now().Format("2006-01-02_15-04-05")
					conflictName := fmt.Sprintf("%s_conflict_%s%s", nameWithoutExt, timestamp, ext)

					if r.safeMode {
						logger.DryRun("Would resolve conflict by uploading %s as %s to %s", path, conflictName, provider)
					} else {
						logger.Info("Resolving conflict by uploading as %s", conflictName)
						jobs = append(jobs, copyJob{
							masterFile: masterFile,
							provider:   provider,
							targetName: conflictName,
							path:       path,
						})
					}
				}
			}
		}
	}

	if len(jobs) == 0 {
		return nil
	}

	// Phase 3: Execute copy jobs in parallel
	const maxWorkers = 4
	logger.Info("Executing %d file copy operations with %d workers...", len(jobs), maxWorkers)

	jobChan := make(chan copyJob, len(jobs))
	for _, j := range jobs {
		jobChan <- j
	}
	close(jobChan)

	var copyWg sync.WaitGroup
	errChan := make(chan error, len(jobs))

	for w := 0; w < maxWorkers; w++ {
		copyWg.Add(1)
		go func() {
			defer copyWg.Done()
			for job := range jobChan {
				if err := r.copyFile(job.masterFile, job.provider, job.targetName); err != nil {
					logger.Error("Failed to copy file %s to %s: %v", job.path, job.provider, err)
					if r.stopOnError {
						errChan <- fmt.Errorf("failed to copy file %s to %s: %w", job.path, job.provider, err)
						return
					}
				}
			}
		}()
	}

	copyWg.Wait()
	close(errChan)

	// Return first error if stopOnError was set
	if err, ok := <-errChan; ok {
		return err
	}

	return nil
}

// enforceSoftDeletedPlacement ensures all replicas of a soft-deleted file are in the soft-deleted folder
func (r *Runner) enforceSoftDeletedPlacement(masterFile *model.File, softDeletedPath string) {
	for _, replica := range masterFile.Replicas {
		if replica.Status != "active" || strings.Contains(replica.Path, softDeletedPath) {
			continue
		}

		logger.Info("Replica for %s on %s is misplaced (found at %s). Moving to soft-deleted...", masterFile.Name, replica.Provider, replica.Path)

		user := r.getUser(replica.Provider, replica.AccountID)
		if user == nil {
			logger.Error("User not found for replica %s", replica.AccountID)
			continue
		}

		client, err := r.GetOrCreateClient(user)
		if err != nil {
			logger.Error("Failed to create client: %v", err)
			continue
		}

		if replica.Provider == model.ProviderTelegram {
			logger.Info("Marking soft-deleted file on Telegram: %s", masterFile.Name)
			if tgClient, ok := client.(*telegram.Client); ok {
				if err := tgClient.UpdateFileStatus(replica, "deleted"); err != nil {
					logger.Error("Failed to update file status on Telegram: %v", err)
				} else {
					replica.Status = "deleted"
					r.db.UpdateReplica(replica)
				}
			} else {
				logger.Error("Client is not a Telegram client for %s", replica.Provider)
			}
		} else {
			destID, err := r.ensureFolderStructure(client, softDeletedPath, replica.Provider)
			if err != nil {
				logger.Error("Failed to ensure soft-deleted folder: %v", err)
				continue
			}

			if r.safeMode {
				logger.DryRun("Would move %s to soft-deleted on %s", masterFile.Name, replica.Provider)
			} else {
				if err := client.MoveFile(replica.NativeID, destID); err != nil {
					logger.Error("Failed to move file to soft-deleted: %v", err)
				} else {
					newPath := "/" + softDeletedPath + "/" + masterFile.Name
					replica.Path = newPath
					r.db.UpdateReplica(replica)
				}
			}
		}
	}
}

// distributeShortcuts ensures that for every file in Microsoft OneDrive,
// all other OneDrive accounts have a shortcut to it.
func (r *Runner) distributeShortcuts(files []*model.File) error {
	logger.Info("Distributing OneDrive shortcuts...")

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

	if len(msUsers) >= 2 {
		r.distributeShortcutsAcrossMSAccounts(msUsers, filesByPath)
	}

	// Distribute Folders (for empty folders)
	if err := r.syncFolderStructures(); err != nil {
		logger.Error("Failed to sync folder structures: %v", err)
	}

	return nil
}

// shortcutJob represents a shortcut creation task
type shortcutJob struct {
	sourceFile *model.File
	user       model.User
	path       string
}

func (r *Runner) distributeShortcutsAcrossMSAccounts(msUsers []model.User, filesByPath map[string][]*model.File) {
	var jobs []shortcutJob

	for path, pathFiles := range filesByPath {
		// Check if this path exists in Microsoft
		var msFiles []*model.File
		for _, f := range pathFiles {
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

		sourceFile := msFiles[0]

		for _, user := range msUsers {
			hasIt := false
			for _, f := range msFiles {
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
				if r.safeMode {
					sourceAccount := ""
					if len(sourceFile.Replicas) > 0 {
						sourceAccount = sourceFile.Replicas[0].AccountID
					}
					logger.DryRun("Would create shortcut for %s in %s -> %s", path, user.Email, sourceAccount)
				} else {
					jobs = append(jobs, shortcutJob{
						sourceFile: sourceFile,
						user:       user,
						path:       path,
					})
				}
			}
		}
	}

	if len(jobs) == 0 {
		return
	}

	const maxWorkers = 4
	logger.Info("Creating %d shortcuts with %d workers...", len(jobs), maxWorkers)

	jobChan := make(chan shortcutJob, len(jobs))
	for _, j := range jobs {
		jobChan <- j
	}
	close(jobChan)

	var wg sync.WaitGroup
	for w := 0; w < maxWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobChan {
				if err := r.createShortcut(job.sourceFile, &job.user); err != nil {
					logger.Error("Failed to create shortcut for %s in %s: %v", job.path, job.user.Email, err)
				}
			}
		}()
	}

	wg.Wait()
}

// syncFolderStructures ensures empty folder structures are replicated across providers
func (r *Runner) syncFolderStructures() error {
	logger.Info("Syncing folder structures...")
	allFolders, err := r.db.GetAllFolders()
	if err != nil {
		return fmt.Errorf("failed to get folders from DB: %w", err)
	}

	paths := make([]string, 0)
	seen := make(map[string]bool)
	for _, f := range allFolders {
		if f.Name == MetadataFileName || f.Name == AuxFolder || f.Name == SoftDeletedFolder || strings.Contains(f.Path, AuxFolder) {
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

		for _, path := range paths {
			_, err := r.ensureFolderStructure(client, path, u.Provider)
			if err != nil && !strings.Contains(err.Error(), "safe mode") {
				logger.Warning("Failed to ensure folder structure for %s: %v", path, err)
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

// GetProviderQuotasFromAPI calculates aggregated quotas for all providers using API
func (r *Runner) GetProviderQuotasFromAPI() ([]*model.ProviderQuota, error) {
	logger.Info("Calculating provider quotas using API (Account Usage)...")

	quotas := make(map[model.Provider]*model.ProviderQuota)
	var mu sync.Mutex

	// Initialize map
	for _, p := range []model.Provider{model.ProviderGoogle, model.ProviderMicrosoft, model.ProviderTelegram} {
		quotas[p] = &model.ProviderQuota{
			Provider: p,
			Total:    0,
			Used:     0,
			Free:     0,
		}
	}

	var wg sync.WaitGroup
	errCh := make(chan error, len(r.config.Users))

	for i := range r.config.Users {
		wg.Add(1)
		go func(user *model.User) {
			defer wg.Done()
			client, err := r.GetOrCreateClient(user)
			if err != nil {
				errCh <- fmt.Errorf("failed to create client for %s: %w", user.Email+user.Phone, err)
				return
			}

			q, err := client.GetQuota()
			if err != nil {
				errCh <- fmt.Errorf("failed to get quota for %s: %w", user.Email+user.Phone, err)
				return
			}

			mu.Lock()
			pq := quotas[user.Provider]
			// Aggregate Used
			pq.Used += q.Used

			// Aggregate Total and Free (skip for main account)
			if !user.IsMain {
				if pq.Total == -1 {
					// Already unlimited, stay unlimited
				} else if q.Total <= 0 {
					// Found an unlimited account, set provider to unlimited
					pq.Total = -1
				} else {
					pq.Total += q.Total
				}
				
				if pq.Free == -1 {
					// Already unlimited
				} else if q.Total <= 0 {
					pq.Free = -1
				} else {
					pq.Free += q.Free
				}
			}
			mu.Unlock()
		}(&r.config.Users[i])
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			return nil, err
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

// GetProviderQuotasFromDB calculates aggregated quotas using DB for usage and API for limits
func (r *Runner) GetProviderQuotasFromDB(updateMetadata bool) ([]*model.ProviderQuota, error) {
	// First, update metadata if requested
	if updateMetadata {
		if err := r.GetMetadata(); err != nil {
			return nil, fmt.Errorf("failed to sync metadata: %w", err)
		}
	}

	// Get base quotas (Limits) from API
	apiQuotas, err := r.GetProviderQuotasFromAPI()
	if err != nil {
		return nil, err
	}

	// Populate SyncFolderUsed from DB
	// SyncFolderUsed reflects pure "Active Sync" data size (filtered by DB).
	// usage from API includes everything (soft-deleted, trash, other files).

	for _, q := range apiQuotas {
		usage, err := r.db.GetProviderUsage(q.Provider)
		if err != nil {
			logger.Warning("Failed to calculate used storage in sync folder for %s: %v", q.Provider, err)
		} else {
			q.SyncFolderUsed = usage
		}
	}

	return apiQuotas, nil
}

// ProcessHardDeletes detects files that are soft-deleted but missing from Google,
// implying a hard delete. It propagates this state to other providers.
func (r *Runner) ProcessHardDeletes() error {
	files, err := r.db.GetFilesByStatus("softdeleted")
	if err != nil {
		return fmt.Errorf("failed to get soft-deleted files: %w", err)
	}

	if len(files) == 0 {
		return nil
	}

	logger.Info("Checking %d soft-deleted files for hard deletion...", len(files))

	for _, file := range files {
		// DEBUG: Log file state for hard delete analysis
		logger.Info("[DEBUG-HD] File ID=%s, Path=%s, CalcID=%s, Status=%s, Replicas=%d",
			file.ID, file.Path, file.CalculatedID, file.Status, len(file.Replicas))
		for _, rep := range file.Replicas {
			logger.Info("[DEBUG-HD]   Replica ID=%d, Provider=%s, Account=%s, NativeID=%s, Status=%s, Path=%s, FileID=%s",
				rep.ID, rep.Provider, rep.AccountID, rep.NativeID, rep.Status, rep.Path, rep.FileID)
		}

		// Check if file is still present in any Google account (active)
		hasGoogleReplica := false
		for _, rep := range file.Replicas {
			if rep.Provider == model.ProviderGoogle && rep.Status == "active" {
				hasGoogleReplica = true
				break
			}
		}

		logger.Info("[DEBUG-HD] hasGoogleReplica=%v for %s", hasGoogleReplica, file.Path)

		// Safety check: query DB directly by calculated_id to catch cases where
		// replicas may not be linked to this file record (file_id mismatch after moves)
		if !hasGoogleReplica && file.CalculatedID != "" {
			found, err := r.db.HasActiveGoogleReplicaOutsideSoftDeleted(file.CalculatedID)
			logger.Info("[DEBUG-HD] Safety check HasActiveGoogleReplicaOutsideSoftDeleted(%s) = %v, err=%v", file.CalculatedID, found, err)
			if err != nil {
				logger.Warning("Failed safety check for %s: %v", file.Path, err)
			} else if found {
				// There's an active Google replica for this content outside soft-deleted.
				// The file was likely restored; mark as active instead of hard-deleting.
				logger.Info("Skipping hard delete for %s: active Google replica found by calculated_id (likely restored)", file.Path)
				file.Status = "active"
				if err := r.db.UpdateFile(file); err != nil {
					logger.Error("Failed to update file status for %s: %v", file.Path, err)
				}
				continue
			}
		}

		if !hasGoogleReplica {
			logger.Info("Detected Hard Delete for %s (CalculatedID: %s). Propagating...", file.Path, file.CalculatedID)

			// 1. Mark as deleted in DB
			file.Status = "deleted"
			if err := r.db.UpdateFile(file); err != nil {
				logger.Error("Failed to update file status for %s: %v", file.Path, err)
				continue
			}

			// 2. Propagate to active and soft-deleted replicas
			for _, rep := range file.Replicas {
				if rep.Status == "deleted" {
					continue
				}

				// Check provider
				if rep.Provider == model.ProviderMicrosoft {
					// Physical Hard Delete
					user := r.getUser(rep.Provider, rep.AccountID)
					if user == nil {
						logger.Warning("User not found for replica %s", rep.AccountID)
						continue
					}

					client, err := r.GetOrCreateClient(user)
					if err != nil {
						logger.Error("Failed to create client for %s: %v", user.Email, err)
						continue
					}

					if r.safeMode {
						logger.DryRun("Would hard delete replica on Microsoft: %s", rep.NativeID)
						continue
					}

					logger.Info("Hard deleting replica on Microsoft: %s", rep.NativeID)
					if err := client.DeleteFile(rep.NativeID); err != nil {
						logger.Error("Failed to delete file on Microsoft: %v", err)
					}

					rep.Status = "deleted"
					if err := r.db.UpdateReplica(rep); err != nil {
						logger.Error("Failed to update replica status: %v", err)
					}

				} else if rep.Provider == model.ProviderTelegram {
					// Update Caption
					user := r.getUser(rep.Provider, rep.AccountID)
					if user == nil {
						continue
					}

					client, err := r.GetOrCreateClient(user)
					if err != nil {
						logger.Error("Failed to create client for Telegram: %v", err)
						continue
					}

					if tgClient, ok := client.(*telegram.Client); ok {
						if r.safeMode {
							logger.DryRun("Would update Telegram caption to 'deleted' for %s", rep.NativeID)
							continue
						}
						logger.Info("Updating Telegram caption to 'deleted' for %s", rep.NativeID)
						if err := tgClient.UpdateFileStatus(rep, "deleted"); err != nil {
							logger.Error("Failed to update Telegram status: %v", err)
						} else {
							rep.Status = "deleted"
							if err := r.db.UpdateReplica(rep); err != nil {
								logger.Error("Failed to update replica status: %v", err)
							}
						}
					}
				}
			}
		}
	}
	return nil
}

// getUser helper
func (r *Runner) getUser(provider model.Provider, accountID string) *model.User {
	for i := range r.config.Users {
		u := &r.config.Users[i]
		// Check provider and ID (Email or Phone)
		match := false
		if u.Provider == provider {
			if provider == model.ProviderTelegram {
				if u.Phone == accountID {
					match = true
				}
			} else {
				if u.Email == accountID {
					match = true
				}
			}
		}
		if match {
			return u
		}
	}
	return nil
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

					// Telegram handling
					if provider == model.ProviderTelegram {
						if tgClient, ok := client.(*telegram.Client); ok {
							logger.Info("Marking file as deleted on Telegram: %s", file.Name)
							if err := tgClient.UpdateFileStatus(targetReplica, "deleted"); err != nil {
								logger.Error("Failed to update file status on Telegram: %v", err)
							} else {
								targetReplica.Status = "deleted"
								r.db.UpdateReplica(targetReplica)
							}
						}
						continue
					}

					// Get target folder ID
					targetFolderID, err := r.ensureFolderStructure(client, targetFolder, provider)
					if err != nil {
						logger.Error("Failed to ensure folder structure for %s: %v", targetFolder, err)
						continue
					}

					if err := client.MoveFile(targetReplica.NativeID, targetFolderID); err != nil {
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
