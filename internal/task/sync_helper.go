package task

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/FranLegon/cloud-drives-sync/internal/api"
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/FranLegon/cloud-drives-sync/internal/microsoft"
	"github.com/FranLegon/cloud-drives-sync/internal/model"
)

// getDestinationClient returns the best client for a provider to upload a file
func (r *Runner) getDestinationClient(provider model.Provider, size int64) (api.CloudClient, *model.User, error) {
	// Find a backup account with space. Start with maxFree = -1.
	var bestUser *model.User
	var bestClient api.CloudClient
	var maxFree int64 = -1

	for i := range r.config.Users {
		user := &r.config.Users[i]
		if user.Provider != provider {
			continue
		}

		// Never upload to Main account
		if user.IsMain {
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

	// Filter viable replicas (ignore shortcuts)
	var viableReplicas []*model.Replica
	for _, rep := range masterFile.Replicas {
		if rep.NativeHash != model.NativeHashShortcut {
			viableReplicas = append(viableReplicas, rep)
		}
	}

	if len(viableReplicas) == 0 {
		return fmt.Errorf("file has no viable replicas (only shortcuts found)")
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

	// 3. Ensure folder structure
	dir := filepath.Dir(masterFile.Path)
	// Normalize path separators
	dir = strings.ReplaceAll(dir, "\\", "/")

	parentID, err := r.ensureFolderStructure(destClient, dir, targetProvider)
	if err != nil {
		return fmt.Errorf("failed to ensure folder structure: %w", err)
	}

	// 4. Transfer file (Try all viable replicas)
	var lastErr error
	for i, sourceReplica := range viableReplicas {
		logger.InfoTagged([]string{string(targetProvider), destUser.Email}, "Copying %s (as %s) from %s (Replica %d/%d)...", masterFile.Path, finalName, sourceReplica.Provider, i+1, len(viableReplicas))

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
			lastErr = fmt.Errorf("failed to get source client for replica %s: %w", sourceReplica.AccountID, err)
			logger.Warning("%v", lastErr)
			continue
		}

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

			if sourceReplica.Fragmented {
				// Fragment logic...
				if len(sourceReplica.Fragments) == 0 {
					errChan <- fmt.Errorf("replica is fragmented but has no fragments")
					return
				}
				for _, frag := range sourceReplica.Fragments {
					if err := sourceClient.DownloadFile(frag.NativeFragmentID, pw); err != nil {
						// Ignore closed pipe error (happens if upload is skipped or finishes early)
						if err != io.ErrClosedPipe && !strings.Contains(err.Error(), "closed pipe") {
							// Downgrade 404 errors
							if strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "notFound") {
								logger.Info("Download source fragment skipped (404): %v", err)
							} else {
								logger.Warning("Download error in pipe (fragment %d): %v", frag.FragmentNumber, err)
							}
							errChan <- err
						}
						return
					}
				}
			} else {
				if err := sourceClient.DownloadFile(sourceReplica.NativeID, pw); err != nil {
					// Ignore closed pipe error (happens if upload is skipped or finishes early)
					if err != io.ErrClosedPipe && !strings.Contains(err.Error(), "closed pipe") {
						// Downgrade 404 errors to Info as they are expected during sync of moved files
						if strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "notFound") {
							logger.Info("Download source skipped (404 Not Found): %v", err)
						} else {
							logger.Warning("Download error in pipe: %v", err)
						}
						errChan <- err
					}
					return // Return early on error
				}
			}
			errChan <- nil // Signal success
			close(errChan)
		}()

		uploadedFile, uploadErr := destClient.UploadFile(parentID, finalName, pr, masterFile.Size)
		// Close the reader to ensure the writer stops if it's still writing
		_ = pr.Close()

		// Check download error
		var downloadErr error
		select {
		case downloadErr = <-errChan:
		default:
			// If channel is empty, it means goroutine hasn't finished or panic
			// But since pr.Close() is called, download should error out or finish
			// Actually best to wait for it.
			downloadErr = <-errChan
		}

		if uploadErr != nil {
			lastErr = fmt.Errorf("upload failed: %w", uploadErr)
			logger.Warning("Copy attempt failed (Upload): %v", lastErr)
			continue
		}

		if downloadErr != nil {
			lastErr = fmt.Errorf("download failed: %w", downloadErr)
			logger.Warning("Copy attempt failed (Download): %v", lastErr)
			// Ensure we rollback the upload if possible?
			// destClient.DeleteFile(uploadedFile.ID) // Optional but good practice
			continue
		}

		// Success! Update Database with new replica
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
			Status:       "active",
			Provider:     targetProvider,
			AccountID:    accountID,
			NativeID:     nativeID,
			NativeHash:   uploadedFile.CalculatedID,
			ModTime:      time.Now(),
			Fragmented:   false,
		}

		if len(uploadedFile.Replicas) > 0 {
			newReplica.Fragmented = uploadedFile.Replicas[0].Fragmented
			newReplica.Fragments = uploadedFile.Replicas[0].Fragments
		}

		if err := r.db.InsertReplica(newReplica); err != nil {
			logger.Error("Failed to insert new replica to DB: %v", err)
		} else {
			// Insert fragments if any
			if newReplica.Fragmented && len(newReplica.Fragments) > 0 {
				for _, frag := range newReplica.Fragments {
					frag.ReplicaID = newReplica.ID
					if err := r.db.InsertReplicaFragment(frag); err != nil {
						logger.Error("Failed to insert fragment into DB: %v", err)
					}
				}
			}
			// Update in-memory masterFile to include new replica
			masterFile.Replicas = append(masterFile.Replicas, newReplica)
		}

		logger.Info("File copied successfully")
		return nil
	}

	return fmt.Errorf("failed to copy file %s after %d attempts. Last error: %v", masterFile.Name, len(viableReplicas), lastErr)
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
		// Attempt to resolve cross-tenant/shared item reference issues for Microsoft
		resolved := false
		if targetUser.Provider == model.ProviderMicrosoft && (strings.Contains(err.Error(), "Invalid request") || strings.Contains(err.Error(), "invalidRequest")) {
			if msClient, ok := targetClient.(*microsoft.Client); ok {
				logger.Info("Shortcut failed. Searching for item in 'Shared with me' to retry (waiting for propagation)...")

				var foundID, foundDriveID string
				// Retry loop for propagation (max 10 seconds)
				for i := 0; i < 5; i++ {
					// Use NativeID or Name to find
					fID, fDID, errSearch := msClient.FindSharedItem(sourceFile.Name, sourceReplica.NativeID)
					if errSearch == nil && fID != "" {
						foundID = fID
						foundDriveID = fDID
						break
					}
					time.Sleep(2 * time.Second)
				}

				if foundID != "" {
					logger.Info("Found shared item! Retrying shortcut creation with ID: %s, DriveID: %s", foundID, foundDriveID)
					shortcut, err = targetClient.CreateShortcut(parentID, sourceFile.Name, foundID, foundDriveID)
					if err == nil {
						resolved = true
					}
				}
			}
		}

		if !resolved {
			if targetUser.Provider == model.ProviderMicrosoft && (strings.Contains(err.Error(), "Invalid request") || strings.Contains(err.Error(), "invalidRequest")) {
				logger.Warning("Shortcut creation failed for %s (likely unsupported cross-account operation): %v. Falling back to placeholder creation.", sourceFile.Name, err)
				if msClient, ok := targetClient.(*microsoft.Client); ok {
					shortcut, err = msClient.CreateFakeShortcut(parentID, sourceFile.Name, sourceFile.Size)
					if err != nil {
						return fmt.Errorf("failed to create fake shortcut: %w", err)
					}
					// Successfully created fake shortcut, proceed to DB update
				} else {
					return fmt.Errorf("failed to cast to Microsoft client for fake shortcut creation")
				}
			} else {
				return fmt.Errorf("failed to create shortcut: %w", err)
			}
		}
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
