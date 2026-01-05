package task

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/FranLegon/cloud-drives-sync/internal/api"
	"github.com/FranLegon/cloud-drives-sync/internal/config"
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/FranLegon/cloud-drives-sync/internal/model"
)

// getDestinationClient returns the best client for a provider to upload a file
func (r *Runner) getDestinationClient(provider model.Provider, size int64) (api.CloudClient, *model.User, error) {
	// For Google, always try Main account first
	if provider == model.ProviderGoogle {
		mainUser := config.GetMainAccount(r.config, model.ProviderGoogle)
		if mainUser != nil {
			client, err := r.GetOrCreateClient(mainUser)
			if err == nil {
				quota, err := client.GetQuota()
				if err == nil && (quota.Total-quota.Used) > size {
					return client, mainUser, nil
				}
			}
		}
	}

	// For others (or if Google Main is full), find a backup account with space
	var bestUser *model.User
	var bestClient api.CloudClient
	var maxFree int64 = -1

	for i := range r.config.Users {
		user := &r.config.Users[i]
		if user.Provider != provider {
			continue
		}

		// Skip main if we already tried it (for Google)
		if provider == model.ProviderGoogle && user.IsMain {
			continue
		}

		client, err := r.GetOrCreateClient(user)
		if err != nil {
			continue
		}

		// Telegram has no quota limit
		if provider == model.ProviderTelegram {
			return client, user, nil
		}

		quota, err := client.GetQuota()
		if err != nil {
			continue
		}

		free := quota.Total - quota.Used
		if free > size && free > maxFree {
			maxFree = free
			bestUser = user
			bestClient = client
		}
	}

	if bestUser != nil {
		return bestClient, bestUser, nil
	}

	return nil, nil, fmt.Errorf("no account found for %s with enough space", provider)
}

// ensureFolderStructure ensures that the folder structure exists in the destination
func (r *Runner) ensureFolderStructure(client api.CloudClient, path string, provider model.Provider) (string, error) {
	// Get root sync folder
	currentID, err := client.GetSyncFolderID()
	if err != nil {
		return "", err
	}

	// Telegram doesn't support folders, so just return the sync channel ID
	if provider == model.ProviderTelegram {
		return currentID, nil
	}

	// Clean path and split
	path = strings.Trim(path, "/\\")
	if path == "" || path == "." {
		return currentID, nil
	}

	parts := strings.Split(path, "/")
	for _, part := range parts {
		if part == "" {
			continue
		}

		// Check if folder exists
		folders, err := client.ListFolders(currentID)
		if err != nil {
			return "", err
		}

		var foundID string
		for _, f := range folders {
			if f.Name == part {
				foundID = f.ID
				break
			}
		}

		if foundID != "" {
			currentID = foundID
		} else {
			// Create folder
			logger.Info("Creating folder '%s'...", part)
			folder, err := client.CreateFolder(currentID, part)
			if err != nil {
				return "", err
			}
			currentID = folder.ID
		}
	}

	return currentID, nil
}

// copyFile copies a file from one provider to another
func (r *Runner) copyFile(masterFile *model.File, targetProvider model.Provider) error {
	// 1. Get source client
	sourceClient, err := r.GetOrCreateClient(&model.User{
		Provider: masterFile.Provider,
		Email:    masterFile.UserEmail,
		Phone:    masterFile.UserPhone,
	})
	if err != nil {
		return fmt.Errorf("failed to get source client: %w", err)
	}

	// 2. Get destination client
	destClient, destUser, err := r.getDestinationClient(targetProvider, masterFile.Size)
	if err != nil {
		return fmt.Errorf("failed to get destination client: %w", err)
	}

	logger.InfoTagged([]string{string(targetProvider), destUser.Email}, "Copying %s from %s...", masterFile.Path, masterFile.Provider)

	// 3. Ensure folder structure
	dir := filepath.Dir(masterFile.Path)
	// Normalize path separators
	dir = strings.ReplaceAll(dir, "\\", "/")

	parentID, err := r.ensureFolderStructure(destClient, dir, targetProvider)
	if err != nil {
		return fmt.Errorf("failed to ensure folder structure: %w", err)
	}

	// 4. Transfer file
	pr, pw := io.Pipe()
	errChan := make(chan error, 1)

	go func() {
		defer pw.Close()
		if err := sourceClient.DownloadFile(masterFile.ID, pw); err != nil {
			errChan <- err
		}
		close(errChan)
	}()

	_, err = destClient.UploadFile(parentID, masterFile.Name, pr, masterFile.Size)
	if err != nil {
		return fmt.Errorf("upload failed: %w", err)
	}

	// Check download error
	if err := <-errChan; err != nil {
		return fmt.Errorf("download failed: %w", err)
	}

	logger.InfoTagged([]string{string(targetProvider), destUser.Email}, "File copied successfully")
	return nil
}
