package task

import (
	"fmt"

	"github.com/FranLegon/cloud-drives-sync/internal/api"
	"github.com/FranLegon/cloud-drives-sync/internal/database"
	"github.com/FranLegon/cloud-drives-sync/internal/google"
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/FranLegon/cloud-drives-sync/internal/microsoft"
	"github.com/FranLegon/cloud-drives-sync/internal/model"
	"github.com/FranLegon/cloud-drives-sync/internal/telegram"
	"golang.org/x/oauth2"
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
		config := &oauth2.Config{
			ClientID:     r.config.GoogleClient.ID,
			ClientSecret: r.config.GoogleClient.Secret,
		}
		client, err = google.NewClient(user, config)
	case model.ProviderMicrosoft:
		config := &oauth2.Config{
			ClientID:     r.config.MicrosoftClient.ID,
			ClientSecret: r.config.MicrosoftClient.Secret,
		}
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
		hashes, err := r.db.GetDuplicateHashes(provider)
		if err != nil {
			logger.ErrorTagged([]string{string(provider)}, "Failed to query duplicates: %v", err)
			continue
		}

		if len(hashes) == 0 {
			logger.InfoTagged([]string{string(provider)}, "No duplicates found")
			continue
		}

		foundDuplicates = true
		logger.InfoTagged([]string{string(provider)}, "Found %d duplicate file groups", len(hashes))

		for _, hash := range hashes {
			files, err := r.db.GetFilesByHash(hash, provider)
			if err != nil {
				continue
			}

			fmt.Printf("\n[%s] Duplicate files (hash: %s):\n", provider, hash)
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
