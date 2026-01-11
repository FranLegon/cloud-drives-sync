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

const (
	AuxFolder        = "sync-cloud-drives-aux"
	MetadataFileName = "metadata.db"
)

func createClient(user *model.User, cfg *model.Config) (api.CloudClient, error) {
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

	// Always run PreFlightCheck to ensure readiness (e.g. Telegram channel initialization)
	if err := c.PreFlightCheck(); err != nil {
		return nil, fmt.Errorf("preflight check failed: %w", err)
	}

	return c, nil
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
		client, err := createClient(user, cfg)
		if err != nil {
			return err
		}

		rootID, err := client.GetSyncFolderID()
		if err != nil {
			return err
		}

		var auxID string
		if user.Provider == model.ProviderTelegram {
			auxID = "/sync-cloud-drives-aux"
		} else {
			folders, err := client.ListFolders(rootID)
			if err != nil {
				return err
			}
			for _, f := range folders {
				if f.Name == AuxFolder {
					auxID = f.ID
					break
				}
			}
		}

		if auxID == "" {
			return fmt.Errorf("aux folder not found")
		}

		files, err := client.ListFiles(auxID)
		if err != nil {
			return err
		}

		var fileID string
		for _, f := range files {
			match := false
			if user.Provider == model.ProviderTelegram {
				// For Telegram, check if the file's path matches the aux folder structure
				if f.Name == MetadataFileName && strings.Contains(f.Path, auxID) {
					match = true
				}
			} else {
				if f.Name == MetadataFileName {
					match = true
				}
			}

			if match {
				fileID = f.ID
				break
			}
		}

		if fileID == "" {
			return fmt.Errorf("metadata.db not found in aux folder")
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
	}

	// Priority 1: Google Main
	for i := range cfg.Users {
		if cfg.Users[i].Provider == model.ProviderGoogle && cfg.Users[i].IsMain {
			if err := tryDownload(&cfg.Users[i]); err == nil {
				return nil
			} else {
				logger.Info("Failed to download from Google Main: %v", err)
			}
		}
	}

	// Priority 2: OneDrive
	for i := range cfg.Users {
		if cfg.Users[i].Provider == model.ProviderMicrosoft {
			if err := tryDownload(&cfg.Users[i]); err == nil {
				return nil
			} else {
				logger.Info("Failed to download from OneDrive: %v", err)
			}
		}
	}

	// Priority 3: Telegram
	for i := range cfg.Users {
		if cfg.Users[i].Provider == model.ProviderTelegram {
			if err := tryDownload(&cfg.Users[i]); err == nil {
				return nil
			} else {
				logger.Info("Failed to download from Telegram: %v", err)
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
		client, err := createClient(user, cfg)
		if err != nil {
			return err
		}

		rootID, err := client.GetSyncFolderID()
		if err != nil {
			return err
		}

		// Find or Create Aux Folder
		var auxID string
		if user.Provider == model.ProviderTelegram {
			// Check if we need to 'create' it or just use the path string.
			// CreateFolder calls return a dummy folder with ID=Path usually.
			// Let's call CreateFolder to be consistent and get the 'ID'.
			f, err := client.CreateFolder(rootID, AuxFolder)
			if err != nil {
				return err
			}
			auxID = f.ID
		} else {
			folders, err := client.ListFolders(rootID)
			if err != nil {
				return err
			}
			for _, f := range folders {
				if f.Name == AuxFolder {
					auxID = f.ID
					break
				}
			}

			if auxID == "" {
				folder, err := client.CreateFolder(rootID, AuxFolder)
				if err != nil {
					return err
				}
				auxID = folder.ID
			}
		}

		// Check for existing metadata.db to overwrite or Create
		files, err := client.ListFiles(auxID)
		if err != nil {
			return err
		}

		var existingFileID string
		for _, f := range files {
			match := false
			if user.Provider == model.ProviderTelegram {
				// For Telegram, check if the file's path matches the aux folder structure
				if f.Name == MetadataFileName && strings.Contains(f.Path, auxID) {
					match = true
				}
			} else {
				if f.Name == MetadataFileName {
					match = true
				}
			}

			if match {
				existingFileID = f.ID
				break
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

		return nil
	}

	// Standardize path separators for logs
	// dbPath logic is already standard

	successCount := 0
	for i := range cfg.Users {
		user := &cfg.Users[i]
		// Skip calling upload if token is known invalid? No, createClient handles refreshing.

		if err := uploadToUser(user); err != nil {
			logger.ErrorTagged([]string{string(user.Provider), user.Email}, "Failed to sync metadata: %v", err)
		} else {
			successCount++
		}
	}

	if successCount == 0 && len(cfg.Users) > 0 {
		return fmt.Errorf("failed to upload metadata.db to any provider")
	}

	return nil
}
