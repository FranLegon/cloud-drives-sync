package google

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/FranLegon/cloud-drives-sync/internal/api"
	"github.com/FranLegon/cloud-drives-sync/internal/auth"
	"github.com/FranLegon/cloud-drives-sync/internal/crypto"
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/FranLegon/cloud-drives-sync/internal/model"
	"golang.org/x/oauth2"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

const (
	syncFolderName = "sync-cloud-drives"
	maxRetries     = 3
	retryDelay     = 2 * time.Second
)

// Client represents a Google Drive client
type Client struct {
	service      *drive.Service
	user         *model.User
	config       *oauth2.Config
	tokenSource  *auth.TokenSource
	syncFolderID string
}

// NewClient creates a new Google Drive client
func NewClient(user *model.User, config *oauth2.Config) (*Client, error) {
	tokenSource := auth.NewTokenSource(config, user.RefreshToken)
	// Ensure token is valid and refreshed if needed
	if _, err := tokenSource.Token(); err != nil {
		return nil, fmt.Errorf("failed to get token: %w", err)
	}

	ctx := context.Background()
	service, err := drive.NewService(ctx, option.WithTokenSource(tokenSource))
	if err != nil {
		return nil, fmt.Errorf("failed to create drive service: %w", err)
	}

	return &Client{
		service:     service,
		user:        user,
		config:      config,
		tokenSource: tokenSource,
	}, nil
}

// PreFlightCheck verifies the sync folder structure
func (c *Client) PreFlightCheck() error {
	var query string
	if c.user.IsMain {
		query = fmt.Sprintf("name='%s' and mimeType='application/vnd.google-apps.folder' and trashed=false and 'me' in owners", syncFolderName)
	} else {
		query = fmt.Sprintf("name='%s' and mimeType='application/vnd.google-apps.folder' and trashed=false and sharedWithMe=true", syncFolderName)
	}

	fileList, err := c.service.Files.List().Q(query).Fields("files(id, name, parents)").Do()
	if err != nil {
		return fmt.Errorf("failed to search for sync folder: %w", err)
	}

	if len(fileList.Files) == 0 {
		return fmt.Errorf("sync folder '%s' not found - run 'init' command first", syncFolderName)
	}

	if len(fileList.Files) > 1 {
		return fmt.Errorf("multiple sync folders found with name '%s' - please resolve manually", syncFolderName)
	}

	folder := fileList.Files[0]
	c.syncFolderID = folder.Id

	// Check if folder is in root, if not move it
	if len(folder.Parents) > 0 {
		logger.InfoTagged([]string{"Google", c.user.Email}, "Moving sync folder to root")
		_, err := c.service.Files.Update(folder.Id, &drive.File{}).AddParents("root").RemoveParents(folder.Parents[0]).Do()
		if err != nil {
			logger.WarningTagged([]string{"Google", c.user.Email}, "Failed to move folder to root: %v", err)
		}
	}

	logger.InfoTagged([]string{"Google", c.user.Email}, "Pre-flight check passed: sync folder '%s' (ID: %s)", syncFolderName, c.syncFolderID)
	return nil
}

// GetSyncFolderID returns the sync folder ID
func (c *Client) GetSyncFolderID() (string, error) {
	if c.syncFolderID == "" {
		if err := c.PreFlightCheck(); err != nil {
			return "", err
		}
	}
	return c.syncFolderID, nil
}

// CreateSyncFolder creates the sync folder in the main account
func (c *Client) CreateSyncFolder() (string, error) {
	folder := &drive.File{
		Name:     syncFolderName,
		MimeType: "application/vnd.google-apps.folder",
		Parents:  []string{"root"},
	}

	createdFolder, err := c.service.Files.Create(folder).Fields("id, name").Do()
	if err != nil {
		return "", fmt.Errorf("failed to create sync folder: %w", err)
	}

	c.syncFolderID = createdFolder.Id
	logger.InfoTagged([]string{"Google", c.user.Email}, "Created sync folder '%s' (ID: %s)", syncFolderName, c.syncFolderID)
	return c.syncFolderID, nil
}

// ListFiles lists files in a folder
func (c *Client) ListFiles(folderID string) ([]*model.File, error) {
	if folderID == "" {
		return nil, errors.New("folder ID is required")
	}

	query := fmt.Sprintf("'%s' in parents and mimeType != 'application/vnd.google-apps.folder' and trashed=false", folderID)

	var allFiles []*model.File
	pageToken := ""

	for {
		call := c.service.Files.List().Q(query).
			Fields("nextPageToken, files(id, name, size, md5Checksum, createdTime, modifiedTime, owners, parents)").
			PageSize(1000)

		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		fileList, err := call.Do()
		if err != nil {
			return nil, fmt.Errorf("failed to list files: %w", err)
		}

		logger.Info("Google ListFiles page: found %d files", len(fileList.Files)) // ADDED LOG

		for _, f := range fileList.Files {
			modTime := parseTime(f.ModifiedTime)
			calculatedID := fmt.Sprintf("%s-%d", f.Name, f.Size)

			// Create the logical file
			file := &model.File{
				ID:           f.Id, // Will be replaced with UUID in database layer
				Name:         f.Name,
				Size:         f.Size,
				Path:         "", // Path will be set by caller based on folder hierarchy
				CalculatedID: calculatedID,
				ModTime:      modTime,
				Status:       "active",
			}

			// Create the replica for this file
			replica := &model.Replica{
				FileID:       "", // Will be set when linking to logical file
				CalculatedID: calculatedID,
				Path:         "", // Path will be set by caller
				Name:         f.Name,
				Size:         f.Size,
				Provider:     model.ProviderGoogle,
				AccountID:    c.user.Email,
				NativeID:     f.Id,
				NativeHash:   f.Md5Checksum,
				ModTime:      modTime,
				Status:       "active",
				Fragmented:   false,
			}

			file.Replicas = []*model.Replica{replica}
			allFiles = append(allFiles, file)
		}

		if fileList.NextPageToken == "" {
			break
		}
		pageToken = fileList.NextPageToken
	}

	return allFiles, nil
}

// ListFolders lists folders in a parent folder
func (c *Client) ListFolders(parentID string) ([]*model.Folder, error) {
	if parentID == "" {
		return nil, errors.New("parent folder ID is required")
	}

	query := fmt.Sprintf("'%s' in parents and mimeType='application/vnd.google-apps.folder' and trashed=false", parentID)

	var allFolders []*model.Folder
	pageToken := ""

	for {
		call := c.service.Files.List().Q(query).
			Fields("nextPageToken, files(id, name, owners, parents)").
			PageSize(1000)

		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		fileList, err := call.Do()
		if err != nil {
			return nil, fmt.Errorf("failed to list folders: %w", err)
		}

		for _, f := range fileList.Files {
			folder := &model.Folder{
				ID:             f.Id,
				Name:           f.Name,
				Provider:       model.ProviderGoogle,
				UserEmail:      c.user.Email,
				ParentFolderID: parentID,
			}

			if len(f.Owners) > 0 {
				folder.OwnerEmail = f.Owners[0].EmailAddress
			}

			allFolders = append(allFolders, folder)
		}

		if fileList.NextPageToken == "" {
			break
		}
		pageToken = fileList.NextPageToken
	}

	return allFolders, nil
}

// DownloadFile downloads a file
func (c *Client) DownloadFile(fileID string, writer io.Writer) error {
	resp, err := c.service.Files.Get(fileID).Download()
	if err != nil {
		return fmt.Errorf("failed to download file: %w", err)
	}
	defer resp.Body.Close()

	logger.InfoTagged([]string{"Google", c.user.Email}, "Download stream started for %s", fileID)

	_, err = io.Copy(writer, resp.Body)

	logger.InfoTagged([]string{"Google", c.user.Email}, "Download stream completed for %s", fileID)
	return err
}

// UploadFile uploads a file
func (c *Client) UploadFile(folderID, name string, reader io.Reader, size int64) (*model.File, error) {
	file := &drive.File{
		Name:    name,
		Parents: []string{folderID},
	}

	createdFile, err := c.service.Files.Create(file).Media(reader).Fields("id, name, size, md5Checksum, createdTime, modifiedTime, owners").Do()
	if err != nil {
		return nil, fmt.Errorf("failed to upload file: %w", err)
	}

	modTime := parseTime(createdFile.ModifiedTime)
	calculatedID := fmt.Sprintf("%s-%d", createdFile.Name, createdFile.Size)

	result := &model.File{
		ID:           createdFile.Id, // Will be replaced with UUID in database layer
		Name:         createdFile.Name,
		Size:         createdFile.Size,
		Path:         "", // Path will be set by caller
		CalculatedID: calculatedID,
		ModTime:      modTime,
		Status:       "active",
	}

	replica := &model.Replica{
		FileID:       "", // Will be set when linking to logical file
		CalculatedID: calculatedID,
		Path:         "", // Path will be set by caller
		Name:         createdFile.Name,
		Size:         createdFile.Size,
		Provider:     model.ProviderGoogle,
		AccountID:    c.user.Email,
		NativeID:     createdFile.Id,
		NativeHash:   createdFile.Md5Checksum,
		ModTime:      modTime,
		Status:       "active",
		Fragmented:   false,
	}

	result.Replicas = []*model.Replica{replica}

	return result, nil
}

// UpdateFile updates file content
func (c *Client) UpdateFile(fileID string, reader io.Reader, size int64) error {
	_, err := c.service.Files.Update(fileID, nil).Media(reader).Do()
	if err != nil {
		return fmt.Errorf("failed to update file content: %w", err)
	}
	return nil
}

// DeleteFile deletes a file
func (c *Client) DeleteFile(fileID string) error {
	err := c.service.Files.Delete(fileID).Do()
	if err != nil {
		// Check if it's a permission error (403)
		var gErr *googleapi.Error
		if errors.As(err, &gErr) && gErr.Code == 403 {
			// Try to trash the file instead
			logger.InfoTagged([]string{"Google"}, "Insufficient permissions to delete file %s, attempting to trash it instead", fileID)
			_, updateErr := c.service.Files.Update(fileID, &drive.File{Trashed: true}).Do()
			return updateErr
		}
		return err
	}
	return nil
}

// MoveFile moves a file to a different folder
func (c *Client) MoveFile(fileID, targetFolderID string) error {
	file, err := c.service.Files.Get(fileID).Fields("parents").Do()
	if err != nil {
		return fmt.Errorf("failed to get file: %w", err)
	}

	updateCall := c.service.Files.Update(fileID, &drive.File{}).AddParents(targetFolderID)

	if len(file.Parents) > 0 {
		updateCall = updateCall.RemoveParents(file.Parents[0])
	}

	_, err = updateCall.Do()

	return err
}

// CreateFolder creates a new folder
func (c *Client) CreateFolder(parentID, name string) (*model.Folder, error) {
	folder := &drive.File{
		Name:     name,
		MimeType: "application/vnd.google-apps.folder",
		Parents:  []string{parentID},
	}

	createdFolder, err := c.service.Files.Create(folder).Fields("id, name, owners").Do()
	if err != nil {
		return nil, fmt.Errorf("failed to create folder: %w", err)
	}

	result := &model.Folder{
		ID:             createdFolder.Id,
		Name:           createdFolder.Name,
		Provider:       model.ProviderGoogle,
		UserEmail:      c.user.Email,
		ParentFolderID: parentID,
	}

	if len(createdFolder.Owners) > 0 {
		result.OwnerEmail = createdFolder.Owners[0].EmailAddress
	}

	return result, nil
}

// EmptySyncFolder recursively deletes all items inside the sync folder and the folder itself.
// It deletes files first, then folders from inner to outer.
func (c *Client) EmptySyncFolder() error {
	folderID, err := c.GetSyncFolderID()
	if err != nil || folderID == "" {
		return nil
	}

	logger.InfoTagged([]string{"Google", c.user.Email}, "Cleaning sync folder %s (Recursive)...", folderID)
	return c.deleteContentsR(folderID)
}

func (c *Client) deleteContentsR(targetID string) error {
	// 1. List all content in the target folder
	query := fmt.Sprintf("'%s' in parents and trashed = false", targetID)

	var allItems []*drive.File
	nextPageToken := ""

	for {
		list, err := c.service.Files.List().Q(query).Fields("nextPageToken, files(id, name, mimeType, owners)").PageSize(100).PageToken(nextPageToken).Do()
		if err != nil {
			return fmt.Errorf("list failed: %w", err)
		}

		allItems = append(allItems, list.Files...)
		nextPageToken = list.NextPageToken
		if nextPageToken == "" {
			break
		}
	}

	// 2. Separate files and folders
	var files []*drive.File
	var folders []*drive.File

	for _, f := range allItems {
		if f.MimeType == "application/vnd.google-apps.folder" {
			folders = append(folders, f)
		} else {
			files = append(files, f)
		}
	}

	// 3. Delete Files first
	for _, f := range files {
		isOwner := false
		for _, o := range f.Owners {
			if o.Me {
				isOwner = true
				break
			}
		}

		if isOwner {
			logger.InfoTagged([]string{"Google", c.user.Email}, "Deleting File %s (%s)", f.Name, f.Id)
			if err := c.DeleteFile(f.Id); err != nil {
				logger.Warning("Failed to delete file %s: %v", f.Name, err)
			}
		}
	}

	// 4. Process Folders (Recurse then Delete)
	for _, f := range folders {
		// Recurse first (Inner)
		if err := c.deleteContentsR(f.Id); err != nil {
			logger.Warning("Failed to recurse into %s: %v", f.Name, err)
		}

		// Then delete the folder (Outer)
		isOwner := false
		for _, o := range f.Owners {
			if o.Me {
				isOwner = true
				break
			}
		}

		if isOwner {
			logger.InfoTagged([]string{"Google", c.user.Email}, "Deleting Folder %s (%s)", f.Name, f.Id)
			if err := c.DeleteFile(f.Id); err != nil {
				logger.Warning("Failed to delete folder %s: %v", f.Name, err)
			}
		}
	}

	// 5. Delete the target folder itself - UPDATED: We don't delete the target folder here.
	// This function is "delete contents of".
	// The caller is responsible for deleting the target folder if desired.
	// This prevents the sync folder root from being deleted/trashed, and fixes the double-delete 404 error in recursion.

	return nil
}

// DeleteFolder deletes a folder
func (c *Client) DeleteFolder(folderID string) error {
	return c.service.Files.Delete(folderID).Do()
}

// ShareFolder shares a folder with an email address
func (c *Client) ShareFolder(folderID, email string, role string) error {
	permission := &drive.Permission{
		Type:         "user",
		Role:         role,
		EmailAddress: email,
	}

	_, err := c.service.Permissions.Create(folderID, permission).SendNotificationEmail(false).Do()
	if err != nil {
		return fmt.Errorf("failed to share folder: %w", err)
	}

	logger.InfoTagged([]string{"Google", c.user.Email}, "Shared folder %s with %s (role: %s)", folderID, email, role)
	return nil
}

// VerifyPermissions verifies that backup accounts have access
func (c *Client) VerifyPermissions() error {
	// This would check that backup accounts have editor access to the sync folder
	// Implementation depends on having access to the config to know backup accounts
	return nil
}

// GetQuota returns storage quota information
func (c *Client) GetQuota() (*api.QuotaInfo, error) {
	about, err := c.service.About.Get().Fields("storageQuota").Do()
	if err != nil {
		return nil, fmt.Errorf("failed to get quota: %w", err)
	}

	quota := &api.QuotaInfo{
		Total: about.StorageQuota.Limit,
		Used:  about.StorageQuota.Usage,
	}

	if quota.Total > 0 {
		quota.Free = quota.Total - quota.Used
	}

	return quota, nil
}

// GetFileMetadata retrieves file metadata
func (c *Client) GetFileMetadata(fileID string) (*model.File, error) {
	f, err := c.service.Files.Get(fileID).Fields("id, name, size, md5Checksum, createdTime, modifiedTime, owners, parents").Do()
	if err != nil {
		return nil, fmt.Errorf("failed to get file metadata: %w", err)
	}

	modTime := parseTime(f.ModifiedTime)
	calculatedID := fmt.Sprintf("%s-%d", f.Name, f.Size)

	file := &model.File{
		ID:           f.Id, // Will be replaced with UUID in database layer
		Name:         f.Name,
		Size:         f.Size,
		Path:         "", // Path will be set by caller
		CalculatedID: calculatedID,
		ModTime:      modTime,
		Status:       "active",
	}

	replica := &model.Replica{
		FileID:       "", // Will be set when linking to logical file
		CalculatedID: calculatedID,
		Path:         "", // Path will be set by caller
		Name:         f.Name,
		Size:         f.Size,
		Provider:     model.ProviderGoogle,
		AccountID:    c.user.Email,
		NativeID:     f.Id,
		NativeHash:   f.Md5Checksum,
		ModTime:      modTime,
		Status:       "active",
		Fragmented:   false,
	}

	file.Replicas = []*model.Replica{replica}

	// If no MD5 hash (e.g., Google Docs), log a warning
	if replica.NativeHash == "" {
		logger.InfoTagged([]string{"Google", c.user.Email}, "No native hash for file %s", f.Name)
	}

	return file, nil
}

// TransferOwnership transfers file ownership
func (c *Client) TransferOwnership(fileID, newOwnerEmail string) error {
	permission := &drive.Permission{
		Type:         "user",
		Role:         "owner",
		EmailAddress: newOwnerEmail,
	}

	// Try direct transfer first
	_, err := c.service.Permissions.Create(fileID, permission).TransferOwnership(true).SendNotificationEmail(true).Do() // I way rather not send email here, but it seems like Google requires it for ownership transfer
	if err == nil {
		logger.InfoTagged([]string{"Google", c.user.Email}, "Transferred ownership of file %s to %s", fileID, newOwnerEmail)
		return nil
	}

	// Check if consent is required
	if isConsentRequiredError(err) {
		logger.InfoTagged([]string{"Google", c.user.Email}, "Direct transfer failed, attempting pending owner flow...")

		// 1. Find existing permission
		var permID string
		perms, err := c.service.Permissions.List(fileID).Fields("permissions(id, emailAddress, role, permissionDetails)").Do()
		if err != nil {
			return fmt.Errorf("failed to list permissions: %w", err)
		}

		for _, p := range perms.Permissions {
			if p.EmailAddress == newOwnerEmail {
				// Check if inherited
				isInherited := false
				for _, pd := range p.PermissionDetails {
					if pd.Inherited {
						isInherited = true
						break
					}
				}

				if !isInherited {
					permID = p.Id
					break
				}
			}
		}

		// 2. If not found, create as writer
		if permID == "" {
			newPerm := &drive.Permission{
				Type:         "user",
				Role:         "writer",
				EmailAddress: newOwnerEmail,
			}
			createdPerm, err := c.service.Permissions.Create(fileID, newPerm).Fields("id").SendNotificationEmail(false).Do()
			if err != nil {
				return fmt.Errorf("failed to create writer permission: %w", err)
			}
			permID = createdPerm.Id
		}

		// 3. Update to pending owner
		updatePerm := &drive.Permission{
			Role:         "owner",
			PendingOwner: true,
		}

		_, err = c.service.Permissions.Update(fileID, permID, updatePerm).TransferOwnership(true).Do()
		if err != nil {
			return fmt.Errorf("failed to set pending owner: %w", err)
		}

		return api.ErrOwnershipTransferPending
	}

	return fmt.Errorf("failed to transfer ownership: %w", err)
}

// AcceptOwnership accepts a pending ownership transfer
func (c *Client) AcceptOwnership(fileID string) error {
	// List permissions to find the pending owner permission for me
	perms, err := c.service.Permissions.List(fileID).Fields("permissions(id, role, emailAddress, pendingOwner)").Do()
	if err != nil {
		return fmt.Errorf("failed to list permissions: %w", err)
	}

	var permID string
	for _, p := range perms.Permissions {
		logger.InfoTagged([]string{"Google", c.user.Email}, "Checking permission: ID=%s, Role=%s, Email=%s, PendingOwner=%v", p.Id, p.Role, p.EmailAddress, p.PendingOwner)

		// Check if this permission is for me and is pending owner
		if p.PendingOwner {
			// If email is present, verify it matches
			if p.EmailAddress != "" && p.EmailAddress != c.user.Email {
				continue
			}
			permID = p.Id
			break
		}
	}

	if permID == "" {
		return errors.New("no pending ownership permission found")
	}

	// Update permission to owner
	_, err = c.service.Permissions.Update(fileID, permID, &drive.Permission{Role: "owner"}).TransferOwnership(true).Do()
	if err != nil {
		return fmt.Errorf("failed to accept ownership: %w", err)
	}

	logger.InfoTagged([]string{"Google", c.user.Email}, "Accepted ownership of file %s", fileID)
	return nil
}

func isConsentRequiredError(err error) bool {
	if err == nil {
		return false
	}
	// Check for specific error string or code
	// "Consent is required to transfer ownership of a file to another user"
	// "consentRequiredForOwnershipTransfer"
	errStr := err.Error()
	return strings.Contains(errStr, "consentRequiredForOwnershipTransfer") ||
		strings.Contains(errStr, "Consent is required")
}

// GetUserEmail returns the user's email
func (c *Client) GetUserEmail() string {
	return c.user.Email
}

// GetUserIdentifier returns the user identifier
func (c *Client) GetUserIdentifier() string {
	return c.user.Email
}

// GetNativeHash retrieves the native hash from the provider
func (c *Client) GetNativeHash(fileID string) (string, string, error) {
	f, err := c.service.Files.Get(fileID).Fields("md5Checksum, sha1Checksum, sha256Checksum").Do()
	if err != nil {
		return "", "", fmt.Errorf("failed to get file hash: %w", err)
	}

	if f.Md5Checksum != "" {
		return f.Md5Checksum, "MD5", nil
	}
	if f.Sha1Checksum != "" {
		return f.Sha1Checksum, "SHA1", nil
	}
	if f.Sha256Checksum != "" {
		return f.Sha256Checksum, "SHA256", nil
	}

	return "", "", errors.New("no native hash available")
}

// CalculateSHA256 calculates SHA-256 hash of a reader
func (c *Client) CalculateSHA256(reader io.Reader) (string, error) {
	return crypto.HashBytes(mustReadAll(reader)), nil
}

func parseTime(timeStr string) time.Time {
	t, _ := time.Parse(time.RFC3339, timeStr)
	return t
}

func mustReadAll(r io.Reader) []byte {
	data, _ := io.ReadAll(r)
	return data
}

// GetDriveID returns the Drive ID (not used for Google)
func (c *Client) GetDriveID() (string, error) {
	return "", nil
}

// CreateShortcut creates a shortcut to a file
func (c *Client) CreateShortcut(parentID, name, targetID, targetDriveID string) (*model.File, error) {
	shortcut := &drive.File{
		Name:     name,
		MimeType: "application/vnd.google-apps.shortcut",
		Parents:  []string{parentID},
		ShortcutDetails: &drive.FileShortcutDetails{
			TargetId: targetID,
		},
	}

	createdShortcut, err := c.service.Files.Create(shortcut).Fields("id, name, mimeType, shortcutDetails").Do()
	if err != nil {
		return nil, fmt.Errorf("failed to create Google Drive shortcut: %w", err)
	}

	// For shortcuts, we create a minimal file entry without replicas
	file := &model.File{
		ID:           createdShortcut.Id,
		Name:         createdShortcut.Name,
		Size:         0,
		Path:         "",
		CalculatedID: fmt.Sprintf("%s-0", createdShortcut.Name),
		ModTime:      time.Now(),
		Status:       "active",
		Replicas:     nil, // Shortcuts don't have physical replicas
	}

	return file, nil
}
