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
	// Telegram has no quota limit, use fast path
	if provider == model.ProviderTelegram {
		for i := range r.config.Users {
			user := &r.config.Users[i]
			if user.Provider == provider && !user.IsMain {
				client, err := r.GetOrCreateClient(user)
				if err == nil {
					return client, user, nil
				}
			}
		}
		return nil, nil, fmt.Errorf("no telegram account found")
	}

	var bestUser *model.User
	var bestClient api.CloudClient
	var maxFree int64 = -1

	for i := range r.config.Users {
		user := &r.config.Users[i]
		if user.Provider != provider || user.IsMain {
			continue
		}

		client, err := r.GetOrCreateClient(user)
		if err != nil {
			continue
		}

		quota, err := r.getQuota(user, client)
		if err != nil {
			logger.Warning("Failed to get quota for %s: %v", user.Email, err)
			continue
		}

		var free int64
		if quota.Total <= 0 {
			free = (1<<63 - 1) - quota.Used
		} else {
			free = quota.Total - quota.Used
		}

		if free > size && free > maxFree {
			maxFree = free
			bestUser = user
			bestClient = client
		}
	}

	if bestUser != nil {
		// Reserve the space for this file
		r.updateQuotaUsed(bestUser, size)
		return bestClient, bestUser, nil
	}

	return nil, nil, fmt.Errorf("no account found for %s with enough space", provider)
}

// transferOwnershipWithFallback transfers ownership and handles the pending state, moving the file to the sync folder if necessary.
func (r *Runner) transferOwnershipWithFallback(sourceClient api.CloudClient, targetClient api.CloudClient, target *AccountStatus, file *model.File, nativeID string, sourceLogTags []string) error {
	err := sourceClient.TransferOwnership(nativeID, target.User.Email)
	if err == api.ErrOwnershipTransferPending {
		logger.InfoTagged(sourceLogTags, "Ownership transfer pending, accepting as %s...", target.User.Email)
		if acceptErr := targetClient.AcceptOwnership(nativeID); acceptErr != nil {
			logger.Error("Failed to accept ownership: %v", acceptErr)
			return fmt.Errorf("acceptance failed: %w", acceptErr)
		}
		err = nil // Clear error as acceptance succeeded

		// Move file to target's sync folder (it's currently in root after pending owner flow)
		dir := model.NormalizePath(filepath.Dir(file.Path))
		targetFolderID, folderErr := r.ensureFolderStructure(targetClient, dir, target.User.Provider)
		if folderErr != nil {
			logger.Warning("Failed to resolve target sync folder for %s: %v", file.Name, folderErr)
		} else {
			if mvErr := targetClient.MoveFile(nativeID, targetFolderID); mvErr != nil {
				logger.Warning("Failed to move transferred file %s to sync folder: %v", file.Name, mvErr)
			} else {
				logger.InfoTagged([]string{string(target.User.Provider), target.User.Email}, "Moved %s to sync folder", file.Name)
			}
		}
	}
	return err
}
// It is safe to call concurrently; a mutex prevents duplicate folder creation.
func (r *Runner) ensureFolderStructure(client api.CloudClient, path string, provider model.Provider) (string, error) {
	// Telegram doesn't support folders, so just return the path
	if provider == model.ProviderTelegram {
		path = model.NormalizePath(path)
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		return path, nil
	}

	// Clean path and split
	path = strings.Trim(path, "/\\")
	accountID := client.GetUserIdentifier()

	cachePrefix := model.GenerateCacheKey(provider, accountID) + ":"

	// Quick fast-path memory cache lookup
	cacheKey := cachePrefix + path
	if cachedID, ok := r.folderCache.Load(cacheKey); ok {
		return cachedID.(string), nil
	}

	// Quick fast-path lookup in DB is redundant since PreloadFolderCache 
	// loads all DB folders into memory, and any new folders are added 
	// to the cache. We only need the memory cache check.

	mu := r.getAccountFolderLock(provider, accountID)
	mu.Lock()
	defer mu.Unlock()

	// Get root sync folder
	currentID, err := client.GetSyncFolderID()
	if err != nil {
		return "", err
	}

	if path == "" || path == "." {
		return currentID, nil
	}

	currentPath := ""
	parts := strings.Split(path, "/")

	for _, part := range parts {
		if part == "" {
			continue
		}

		parentPath := currentPath
		if currentPath == "" {
			currentPath = part
		} else {
			currentPath += "/" + part
		}

		// First check memory cache
		partCacheKey := cachePrefix + currentPath
		if cachedID, ok := r.folderCache.Load(partCacheKey); ok {
			currentID = cachedID.(string)
			continue
		}

		// DB fallback is redundant because PreloadFolderCache already loaded all folders
		// and newly created folders are also added to the cache.

		// Fallback to API if not in DB (or if it was just created by another thread/process and not synced yet)
		folders, err := client.ListFolders(currentID)
		if err != nil {
			return "", err
		}

		var foundID string
		for _, f := range folders {
			siblingPath := f.Name
			if parentPath != "" {
				siblingPath = parentPath + "/" + f.Name
			}
			siblingCacheKey := cachePrefix + siblingPath
			r.folderCache.Store(siblingCacheKey, f.ID)

			if f.Name == part {
				foundID = f.ID
			}
		}

		if foundID != "" {
			parentID := currentID
			currentID = foundID
			// Opportunistically insert to DB for subsequent lookups
			r.db.InsertFolder(&model.Folder{
				ID:             foundID,
				Name:           part,
				Path:           "/" + currentPath,
				Provider:       provider,
				UserEmail:      client.GetUserEmail(),
				UserPhone:      accountID, // fallback
				ParentFolderID: parentID,
			})
		} else {
			// Create folder
			if r.safeMode {
				logger.DryRun("Would create folder %q in path %q", part, currentPath)
				return "", fmt.Errorf("safe mode: skipped folder creation for %s", part)
			}
			logger.Info("Creating folder %q in path %q...", part, currentPath)
			folder, err := client.CreateFolder(currentID, part)
			if err != nil {
				return "", err
			}
			currentID = folder.ID

			// Insert newly created folder into DB
			folder.Path = "/" + currentPath
			folder.Provider = provider
			folder.UserEmail = client.GetUserEmail()
			if provider == model.ProviderTelegram {
				folder.UserPhone = accountID
			}
			r.db.InsertFolder(folder)
			r.folderCache.Store(partCacheKey, currentID)
		}
	}

	r.folderCache.Store(cacheKey, currentID)
	return currentID, nil
}

func handleDownloadError(err error, errChan chan error, contextStr string) {
	if err != io.ErrClosedPipe && !strings.Contains(err.Error(), "closed pipe") {
		if strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "notFound") {
			logger.Info("Download source skipped (404 Not Found) %s: %v", contextStr, err)
		} else {
			logger.Warning("Download error in pipe %s: %v", contextStr, err)
		}
		errChan <- err
	}
}

// copyFile copies a file from one provider to another.
// syncRunID is used to checkpoint the copy for crash recovery; pass 0 to disable.
func (r *Runner) copyFile(masterFile *model.File, targetProvider model.Provider, targetName string, syncRunID int64) error {
	// 1. Get source replica to determine which client to use
	if len(masterFile.Replicas) == 0 {
		return fmt.Errorf("file has no replicas")
	}

	// Filter viable replicas (ignore shortcuts and deleted replicas)
	var viableReplicas []*model.Replica
	for _, rep := range masterFile.Replicas {
		if rep.NativeHash != model.NativeHashShortcut && rep.Status == "active" {
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
	dir := model.NormalizePath(filepath.Dir(masterFile.Path))

	parentID, err := r.ensureFolderStructure(destClient, dir, targetProvider)
	if err != nil {
		return fmt.Errorf("failed to ensure folder structure: %w", err)
	}

	// 4. Transfer file (Try all viable replicas)
	var lastErr error
	for i, sourceReplica := range viableReplicas {
		logger.InfoTagged(destUser.LogTags(), "Copying path=%q (as %s) from_provider=%s native_id=%s (Replica %d/%d)...", masterFile.Path, finalName, sourceReplica.Provider, sourceReplica.NativeID, i+1, len(viableReplicas))

		// Get source client
		sourceUser := r.getUser(sourceReplica.Provider, sourceReplica.AccountID)
		if sourceUser == nil {
			lastErr = fmt.Errorf("user not found for replica %s", sourceReplica.AccountID)
			logger.Warning("Copy failed (user not found) path=%q provider=%s account=%s: %v", masterFile.Path, sourceReplica.Provider, sourceReplica.AccountID, lastErr)
			continue
		}

		sourceClient, err := r.GetOrCreateClient(sourceUser)
		if err != nil {
			lastErr = fmt.Errorf("failed to get source client for replica %s: %w", sourceReplica.AccountID, err)
			logger.Warning("Copy failed (client init) path=%q provider=%s account=%s: %v", masterFile.Path, sourceReplica.Provider, sourceReplica.AccountID, lastErr)
			continue
		}

		pr, pw := io.Pipe()
		defer pr.Close() // Ensure reader is closed to prevent goroutine leaks if upload fails early
		errChan := make(chan error, 1)

		go func() {
			var dlErr error
			defer func() {
				if r := recover(); r != nil {
					logger.Error("Panic in download goroutine path=%q provider=%s native_id=%s: %v", masterFile.Path, sourceReplica.Provider, sourceReplica.NativeID, r)
					pw.CloseWithError(fmt.Errorf("panic: %v", r))
				} else if dlErr != nil {
					pw.CloseWithError(dlErr)
				} else {
					pw.Close()
				}
			}()

			if sourceReplica.Fragmented {
				// Fragment logic...
				if len(sourceReplica.Fragments) == 0 {
					dlErr = fmt.Errorf("replica is fragmented but has no fragments")
					errChan <- dlErr
					return
				}
				for _, frag := range sourceReplica.Fragments {
					if err := sourceClient.DownloadFile(frag.NativeFragmentID, pw); err != nil {
						dlErr = err
						handleDownloadError(err, errChan, fmt.Sprintf("(fragment %d)", frag.FragmentNumber))
						return
					}
				}
			} else {
				if err := sourceClient.DownloadFile(sourceReplica.NativeID, pw); err != nil {
					dlErr = err
					handleDownloadError(err, errChan, "")
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
			logger.Warning("Copy upload failed path=%q provider=%s native_id=%s: %v", masterFile.Path, targetProvider, sourceReplica.NativeID, lastErr)
			continue
		}

		if downloadErr != nil {
			lastErr = fmt.Errorf("download failed: %w", downloadErr)
			logger.Warning("Copy download failed path=%q provider=%s native_id=%s: %v", masterFile.Path, sourceReplica.Provider, sourceReplica.NativeID, lastErr)
			// Ensure we rollback the upload if possible?
			// destClient.DeleteFile(uploadedFile.ID) // Optional but good practice
			continue
		}

		// Success! Update Database with new replica
		accountID := destUser.GetAccountID()

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
			logger.Error("Failed to insert new replica to DB path=%q provider=%s native_id=%s: %v", masterFile.Path, targetProvider, nativeID, err)
		} else {
			// Update in-memory masterFile to include new replica
			masterFile.Replicas = append(masterFile.Replicas, newReplica)

			// Checkpoint successful copy for crash recovery
			if syncRunID > 0 {
				if err := r.db.LogSyncCopy(syncRunID, masterFile.ID, string(targetProvider)); err != nil {
					logger.Warning("Failed to log sync copy checkpoint path=%q provider=%s native_id=%s: %v", masterFile.Path, targetProvider, nativeID, err)
				}
			}
		}

		return nil
	}

	return fmt.Errorf("failed to copy file %s after %d attempts. Last error: %v", masterFile.Name, len(viableReplicas), lastErr)
}

// createShortcut shares the source file and creates a shortcut in the target account
func (r *Runner) createShortcut(sourceFile *model.File, targetUser *model.User, syncRunID int64) error {
	// 1. Find a compatible source replica
	if len(sourceFile.Replicas) == 0 {
		return fmt.Errorf("file has no replicas")
	}

	var sourceReplica *model.Replica
	for _, replica := range sourceFile.Replicas {
		if replica.Provider == targetUser.Provider && replica.Status == "active" {
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

	// 2. Share Source File with Target User (with cache check for Microsoft)
	shareSkipped := false
	if targetUser.Provider == model.ProviderMicrosoft {
		cacheKey := fmt.Sprintf("%s:%s", sourceReplica.AccountID, targetUser.Email)
		r.msShareFailureCacheMu.RLock()
		if r.msShareFailureCache[cacheKey] {
			logger.InfoTagged(sourceReplica.LogTags(), "Skipping share path=%q with target=%s (cached failure)", sourceFile.Path, targetUser.Email)
			shareSkipped = true
		}
		r.msShareFailureCacheMu.RUnlock()
	}

	if !shareSkipped {
		logger.InfoTagged(sourceReplica.LogTags(), "Sharing path=%q with target=%s native_id=%s...", sourceFile.Path, targetUser.Email, sourceReplica.NativeID)
		if err := sourceClient.ShareFolder(sourceReplica.NativeID, targetUser.Email, "reader"); err != nil {
			logger.Warning("Share failed (attempting shortcut anyway) path=%q target=%s native_id=%s: %v", sourceFile.Path, targetUser.Email, sourceReplica.NativeID, err)
			// Cache the failure for Microsoft accounts to avoid retrying
			if targetUser.Provider == model.ProviderMicrosoft && strings.Contains(err.Error(), "There was a problem sharing") {
				cacheKey := fmt.Sprintf("%s:%s", sourceReplica.AccountID, targetUser.Email)
				r.msShareFailureCacheMu.Lock()
				r.msShareFailureCache[cacheKey] = true
				r.msShareFailureCacheMu.Unlock()
				logger.InfoTagged(sourceReplica.LogTags(), "Cached sharing failure path=%q account=%s target=%s", sourceFile.Path, sourceReplica.AccountID, targetUser.Email)
			}
		}
	}

	// 3. Get Target Client
	targetClient, err := r.GetOrCreateClient(targetUser)
	if err != nil {
		return fmt.Errorf("failed to get target client: %w", err)
	}

	// Fetch source Drive ID (needed for MS OneDrive shortcuts)
	sourceDriveID, err := sourceClient.GetDriveID()
	if err != nil {
		logger.Warning("Failed to get source drive ID path=%q provider=%s: %v", sourceFile.Path, sourceReplica.Provider, err)
	}
	if sourceDriveID == "" && targetUser.Provider == model.ProviderMicrosoft {
		logger.Warning("Source Drive ID is empty, but required for Microsoft shortcut creation path=%q provider=%s", sourceFile.Path, sourceReplica.Provider)
	}

	logger.InfoTagged(targetUser.LogTags(), "Creating shortcut path=%q native_id=%s source_drive_id=%s...", sourceFile.Path, sourceReplica.NativeID, sourceDriveID)

	// 4. Ensure Folder Structure in Target
	dir := model.NormalizePath(filepath.Dir(sourceFile.Path))

	parentID, err := r.ensureFolderStructure(targetClient, dir, targetUser.Provider)
	if err != nil {
		return fmt.Errorf("failed to ensure folder structure: %w", err)
	}

	// 5. Create Shortcut
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
				logger.Warning("Shortcut creation failed path=%q native_id=%s (likely unsupported cross-account operation): %v. Falling back to placeholder creation.", sourceFile.Path, sourceReplica.NativeID, err)
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
	accountID := targetUser.GetAccountID()
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
		logger.Error("DB insert shortcut replica failed path=%q provider=%s account=%s: %v", sourceFile.Path, targetUser.Provider, accountID, err)
	} else {
		if syncRunID > 0 {
			targetProviderKey := fmt.Sprintf("shortcut:%s", targetUser.Email)
			if err := r.db.LogSyncCopy(syncRunID, sourceFile.ID, targetProviderKey); err != nil {
				logger.Warning("Failed to log shortcut creation checkpoint path=%q provider=%s account=%s: %v", sourceFile.Path, targetUser.Provider, accountID, err)
			}
		}
	}

	return nil
}
