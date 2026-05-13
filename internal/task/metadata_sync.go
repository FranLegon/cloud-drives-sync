package task

import (
	"fmt"
	"os"
	"strings"

	"github.com/FranLegon/cloud-drives-sync/internal/api"
	"github.com/FranLegon/cloud-drives-sync/internal/auth"
	"github.com/FranLegon/cloud-drives-sync/internal/google"
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/FranLegon/cloud-drives-sync/internal/microsoft"
	"github.com/FranLegon/cloud-drives-sync/internal/model"
	"github.com/FranLegon/cloud-drives-sync/internal/telegram"
)

var AuxFolder = "cloud-drives-sync-aux"

const (
	SoftDeletedFolder = "soft-deleted"
	MetadataFileName  = "metadata.db"
)

// SetAuxFolder overrides the auxiliary folder name. Used by tests to isolate from production data.
func SetAuxFolder(name string) {
	AuxFolder = name
}

func createClient(user *model.User, cfg *model.Config, runPreFlight bool) (api.CloudClient, error) {
	// Re-use the same factory logic as Runner.GetOrCreateClient but without caching,
	// since this is called during startup before a Runner is available.
	var c api.CloudClient
	var err error

	switch user.Provider {
	case model.ProviderGoogle:
		oauthConfig := auth.GetGoogleOAuthConfig(cfg.GoogleClient.ID, cfg.GoogleClient.Secret)
		c, err = google.NewClient(user, oauthConfig)
	case model.ProviderMicrosoft:
		oauthConfig := auth.GetMicrosoftOAuthConfig(cfg.MicrosoftClient.ID, cfg.MicrosoftClient.Secret)
		c, err = microsoft.NewClient(user, oauthConfig)
	case model.ProviderTelegram:
		c, err = telegram.NewClient(user, cfg.TelegramClient.APIID, cfg.TelegramClient.APIHash)
	default:
		return nil, fmt.Errorf("unsupported provider: %s", user.Provider)
	}

	if err != nil {
		return nil, err
	}

	if runPreFlight {
		if err := c.PreFlightCheck(); err != nil {
			return nil, fmt.Errorf("preflight check failed: %w", err)
		}
	}

	return c, nil
}

func getAuxFolderID(client api.CloudClient, user *model.User, rootID string, create bool) (string, error) {
	if user.Provider == model.ProviderTelegram {
		if create {
			f, err := client.CreateFolder(rootID, AuxFolder)
			if err != nil {
				return "", err
			}
			return f.ID, nil
		}
		return "/" + AuxFolder, nil
	}

	folders, err := client.ListFolders(rootID)
	if err != nil {
		return "", err
	}
	for _, f := range folders {
		if f.Name == AuxFolder {
			return f.ID, nil
		}
	}

	if create {
		folder, err := client.CreateFolder(rootID, AuxFolder)
		if err != nil {
			return "", err
		}
		return folder.ID, nil
	}
	return "", fmt.Errorf("aux folder not found")
}

func getMetadataFileID(client api.CloudClient, user *model.User, auxID string) (string, error) {
	files, err := client.ListFiles(auxID)
	if err != nil {
		return "", err
	}
	for _, f := range files {
		match := false
		if user.Provider == model.ProviderTelegram {
			if f.Name == MetadataFileName && strings.Contains(f.Path, auxID) {
				match = true
			}
		} else {
			if f.Name == MetadataFileName {
				match = true
			}
		}
		if match {
			return f.ID, nil
		}
	}
	return "", fmt.Errorf("metadata.db not found in aux folder")
}

// DownloadMetadataDB attempts to download metadata.db from providers
func DownloadMetadataDB(cfg *model.Config, dbPath string) error {
	// Check existence
	if _, err := os.Stat(dbPath); err == nil {
		logger.Info("Local metadata.db found.")
		return nil
	}

	logger.Info("Local metadata.db missing. Attempting to download from cloud providers...")

	tryDownload := func(user *model.User) error {
		return api.WithRetry(func() error {
			client, err := createClient(user, cfg, true)
		if err != nil {
			return err
		}

		rootID, err := client.GetSyncFolderID()
		if err != nil {
			return err
		}

		auxID, err := getAuxFolderID(client, user, rootID, false)
		if err != nil {
			return err
		}

		fileID, err := getMetadataFileID(client, user, auxID)
		if err != nil {
			return err
		}

		// Create file locally
		out, err := os.Create(dbPath)
		if err != nil {
			return err
		}
		defer out.Close()

		logger.Info("Downloading metadata.db from %s (%s)...", user.Provider, user.Email)
		if err := client.DownloadFile(fileID, out); err != nil {
				out.Close()
				os.Remove(dbPath) // Clean up partial
				return err
			}
			return nil
		})
	}

	priorities := []struct {
		check func(u *model.User) bool
		name  string
	}{
		{func(u *model.User) bool { return u.Provider == model.ProviderGoogle && u.IsMain }, "Google Main"},
		{func(u *model.User) bool { return u.Provider == model.ProviderMicrosoft }, "OneDrive"},
		{func(u *model.User) bool { return u.Provider == model.ProviderTelegram }, "Telegram"},
	}

	for _, p := range priorities {
		for i := range cfg.Users {
			if p.check(&cfg.Users[i]) {
				if err := tryDownload(&cfg.Users[i]); err == nil {
					return nil
				} else {
					logger.Info("Failed to download from %s: %v", p.name, err)
				}
			}
		}
	}

	return fmt.Errorf("metadata.db not found locally or in any cloud provider")
}

// UploadMetadataDB uploads the local metadata.db to all providers
func UploadMetadataDB(cfg *model.Config, dbPath string) error {
	file, err := os.Open(dbPath)
	if err != nil {
		return fmt.Errorf("failed to open metadata.db: %w", err)
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return err
	}
	size := stat.Size()

	logger.Info("Uploading metadata.db to cloud providers...")

	uploadToUser := func(user *model.User) error {
		return api.WithRetry(func() error {
			client, err := createClient(user, cfg, true)
		if err != nil {
			return err
		}

		rootID, err := client.GetSyncFolderID()
		if err != nil {
			return err
		}

		// Find or Create Aux Folder
		auxID, err := getAuxFolderID(client, user, rootID, true)
		if err != nil {
			return err
		}

		// Check for existing metadata.db to overwrite or Create
		// We do this BEFORE ListFolders(auxID) so that if getMetadataFileID (which calls ListFiles)
		// succeeds, it populates the folder cache and saves ListFolders an API call.
		existingFileID, err := getMetadataFileID(client, user, auxID)
		if err != nil && !strings.Contains(err.Error(), "not found in aux folder") {
			return err
		}

		// Ensure soft-deleted folder exists
		if user.Provider == model.ProviderTelegram {
			if _, err := client.CreateFolder(auxID, SoftDeletedFolder); err != nil {
				return fmt.Errorf("failed to create soft-deleted folder: %w", err)
			}
		} else {
			folders, err := client.ListFolders(auxID)
			if err != nil {
				return fmt.Errorf("failed to list folders in aux: %w", err)
			}
			foundSoftDeleted := false
			for _, f := range folders {
				if f.Name == SoftDeletedFolder {
					foundSoftDeleted = true
					break
				}
			}
			if !foundSoftDeleted {
				if _, err := client.CreateFolder(auxID, SoftDeletedFolder); err != nil {
					return fmt.Errorf("failed to create soft-deleted folder: %w", err)
				}
			}
		}

		if _, err := file.Seek(0, 0); err != nil {
			return err
		}

		if existingFileID != "" {
			logger.Info("Updating existing metadata.db on %s (%s)...", user.Provider, user.Email)
			if err := client.UpdateFile(existingFileID, file, size); err != nil {
				return fmt.Errorf("failed to update metadata.db: %w", err)
			}
		} else {
				logger.Info("Uploading new metadata.db to %s (%s)...", user.Provider, user.Email)
				if _, err := client.UploadFile(auxID, MetadataFileName, file, size); err != nil {
					return fmt.Errorf("failed to upload metadata.db: %w", err)
				}
			}

			return nil
		})
	}

	// Standardize path separators for logs
	// dbPath logic is already standard

	successCount := 0
	for i := range cfg.Users {
		user := &cfg.Users[i]
		// Skip calling upload if token is known invalid? No, createClient handles refreshing.

		if err := uploadToUser(user); err != nil {
			logger.ErrorTagged(user.LogTags(), "Failed to sync metadata: %v", err)
		} else {
			successCount++
		}
	}

	if successCount == 0 && len(cfg.Users) > 0 {
		return fmt.Errorf("failed to upload metadata.db to any provider")
	}

	return nil
}
