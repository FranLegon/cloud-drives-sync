package task

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

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
		// For Telegram, we return the path itself as the "ID" so it can be stored in metadata
		// Ensure path starts with /
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		// Replace backslashes with forward slashes
		path = strings.ReplaceAll(path, "\\", "/")
		return path, nil
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
func (r *Runner) copyFile(masterFile *model.File, targetProvider model.Provider, targetName string) error {
	// 1. Get source replica to determine which client to use
	if len(masterFile.Replicas) == 0 {
		return fmt.Errorf("file has no replicas")
	}

	// Use the first replica as the source
	sourceReplica := masterFile.Replicas[0]

	// Get source client
	var email, phone string
	if sourceReplica.Provider == model.ProviderTelegram {
		phone = sourceReplica.AccountID
	} else {
		email = sourceReplica.AccountID
	}

	sourceClient, err := r.GetOrCreateClient(&model.User{
		Provider: sourceReplica.Provider,
		Email:    email,
		Phone:    phone,
	})
	if err != nil {
		return fmt.Errorf("failed to get source client: %w", err)
	}

	// 2. Get destination client
	destClient, destUser, err := r.getDestinationClient(targetProvider, masterFile.Size)
	if err != nil {
		return fmt.Errorf("failed to get destination client: %w", err)
	}

	finalName := masterFile.Name
	if targetName != "" {
		finalName = targetName
	}

	logger.InfoTagged([]string{string(targetProvider), destUser.Email}, "Copying %s (as %s) from %s...", masterFile.Path, finalName, sourceReplica.Provider)

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
		defer func() {
			if r := recover(); r != nil {
				logger.Error("Panic in download goroutine: %v", r)
				pw.CloseWithError(fmt.Errorf("panic: %v", r))
			} else {
				pw.Close()
			}
		}()

		if err := sourceClient.DownloadFile(sourceReplica.NativeID, pw); err != nil {
			// Ignore closed pipe error (happens if upload is skipped or finishes early)
			if err != io.ErrClosedPipe && !strings.Contains(err.Error(), "closed pipe") {
				logger.Warning("Download error in pipe: %v", err)
				errChan <- err
			}
		}
		close(errChan)
	}()

	uploadedFile, err := destClient.UploadFile(parentID, finalName, pr, masterFile.Size)
	// Close the reader to ensure the writer stops if it's still writing
	_ = pr.Close()

	if err != nil {
		return fmt.Errorf("upload failed: %w", err)
	}

	// Check download error
	if err := <-errChan; err != nil {
		return fmt.Errorf("download failed: %w", err)
	}

	// Update Database with new replica
	accountID := destUser.Email
	if targetProvider == model.ProviderTelegram {
		accountID = destUser.Phone
	}

	// Determine NativeID from uploaded result
	nativeID := uploadedFile.ID
	if len(uploadedFile.Replicas) > 0 && uploadedFile.Replicas[0].NativeID != "" {
		nativeID = uploadedFile.Replicas[0].NativeID
	}

	newReplica := &model.Replica{
		FileID:       masterFile.ID,
		CalculatedID: masterFile.CalculatedID,
		Path:         masterFile.Path,
		Name:         uploadedFile.Name,
		Size:         uploadedFile.Size,
		Provider:     targetProvider,
		AccountID:    accountID,
		NativeID:     nativeID,
		ModTime:      time.Now(),
		Status:       "active",
		Fragmented:   false, // Default safely, check below
	}

	// Copy fragmentation info if present in uploaded result
	if len(uploadedFile.Replicas) > 0 {
		newReplica.Fragmented = uploadedFile.Replicas[0].Fragmented
		newReplica.Fragments = uploadedFile.Replicas[0].Fragments
	}

	if err := r.db.InsertReplica(newReplica); err != nil {
		logger.Error("Failed to insert new replica into DB: %v", err)
		// Don't fail the operation, but consistency is compromised
	} else {
		logger.Info("Replica recorded in DB for %s on %s", masterFile.Path, targetProvider)

		// Insert fragments if any
		if newReplica.Fragmented && len(newReplica.Fragments) > 0 {
			// Update Replica ID for fragments (assigned by DB)
			for _, frag := range newReplica.Fragments {
				frag.ReplicaID = newReplica.ID
				if err := r.db.InsertReplicaFragment(frag); err != nil {
					logger.Error("Failed to insert fragment into DB: %v", err)
				}
			}
		}
	}

	logger.InfoTagged([]string{string(targetProvider), destUser.Email}, "File copied successfully")
	return nil
}

// createShortcut shares the source file and creates a shortcut in the target account
func (r *Runner) createShortcut(sourceFile *model.File, targetUser *model.User) error {
	// 1. Find a compatible source replica
	if len(sourceFile.Replicas) == 0 {
		return fmt.Errorf("file has no replicas")
	}

	var sourceReplica *model.Replica
	for _, replica := range sourceFile.Replicas {
		if replica.Provider == targetUser.Provider {
			sourceReplica = replica
			break
		}
	}

	if sourceReplica == nil {
		// Fallback: Use first replica (might work for some providers or if cross-linking supported later)
		// But for now, warn and return error if strictly required.
		// Actually, logging a warning and returning error is better than using wrong provider.
		return fmt.Errorf("no source replica found for provider %s", targetUser.Provider)
	}

	// Get Source Client
	var email, phone string
	if sourceReplica.Provider == model.ProviderTelegram {
		phone = sourceReplica.AccountID
	} else {
		email = sourceReplica.AccountID
	}

	sourceClient, err := r.GetOrCreateClient(&model.User{
		Provider: sourceReplica.Provider,
		Email:    email,
		Phone:    phone,
	})
	if err != nil {
		return fmt.Errorf("failed to get source client: %w", err)
	}

	// 2. Share Source File with Target User
	logger.InfoTagged([]string{string(sourceReplica.Provider), sourceReplica.AccountID}, "Sharing %s with %s...", sourceFile.Name, targetUser.Email)
	if err := sourceClient.ShareFolder(sourceReplica.NativeID, targetUser.Email, "reader"); err != nil {
		logger.Warning("Share failed (attempting shortcut anyway): %v", err)
	}

	// 3. Get Target Client
	targetClient, err := r.GetOrCreateClient(targetUser)
	if err != nil {
		return fmt.Errorf("failed to get target client: %w", err)
	}

	// Fetch source Drive ID (needed for MS OneDrive shortcuts)
	sourceDriveID, err := sourceClient.GetDriveID()
	if err != nil {
		logger.Warning("Failed to get source drive ID: %v", err)
	}
	if sourceDriveID == "" && targetUser.Provider == model.ProviderMicrosoft {
		logger.Warning("Source Drive ID is empty, but required for Microsoft shortcut creation. Source Provider: %s", sourceReplica.Provider)
	}

	logger.InfoTagged([]string{string(targetUser.Provider), targetUser.Email}, "Creating shortcut for %s (TargetID: %s, DriveID: %s)...", sourceFile.Name, sourceReplica.NativeID, sourceDriveID)

	// 4. Ensure Folder Structure in Target
	dir := filepath.Dir(sourceFile.Path)
	dir = strings.ReplaceAll(dir, "\\", "/")

	parentID, err := r.ensureFolderStructure(targetClient, dir, targetUser.Provider)
	if err != nil {
		return fmt.Errorf("failed to ensure folder structure: %w", err)
	}

	// 5. Create Shortcut
	logger.InfoTagged([]string{string(targetUser.Provider), targetUser.Email}, "Creating shortcut for %s...", sourceFile.Name)
	shortcut, err := targetClient.CreateShortcut(parentID, sourceFile.Name, sourceReplica.NativeID, sourceDriveID)
	if err != nil {
		if targetUser.Provider == model.ProviderMicrosoft && (strings.Contains(err.Error(), "Invalid request") || strings.Contains(err.Error(), "invalidRequest")) {
			logger.Warning("Shortcut creation failed for %s (likely unsupported cross-account operation): %v. Falling back to simple copy.", sourceFile.Name, err)
			return r.copyFile(sourceFile, targetUser.Provider, sourceFile.Name)
		}
		return fmt.Errorf("failed to create shortcut: %w", err)
	}

	// 6. Update Database with new replica (Shortcut)
	accountID := targetUser.Email
	if targetUser.Provider == model.ProviderTelegram {
		accountID = targetUser.Phone
	}
	// For shortcuts, we might treat size as 0 or the original size.
	// Microsoft shortcut usually has size 0 in our model unless we fetched it.

	newReplica := &model.Replica{
		FileID:       sourceFile.ID,
		CalculatedID: sourceFile.CalculatedID,
		Path:         sourceFile.Path,
		Name:         shortcut.Name,
		Size:         shortcut.Size,
		Provider:     targetUser.Provider,
		AccountID:    accountID,
		NativeID:     shortcut.ID,
		ModTime:      time.Now(),
		Status:       "active",
	}

	if err := r.db.InsertReplica(newReplica); err != nil {
		logger.Error("Failed to insert shortcut replica into DB: %v", err)
	} else {
		logger.Info("Shortcut replica recorded in DB for %s on %s", sourceFile.Path, targetUser.Provider)
	}

	return nil
}
