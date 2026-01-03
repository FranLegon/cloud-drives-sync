package google

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/FranLegon/cloud-drives-sync/internal/api"
	"github.com/FranLegon/cloud-drives-sync/internal/auth"
	"github.com/FranLegon/cloud-drives-sync/internal/crypto"
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/FranLegon/cloud-drives-sync/internal/model"
	"golang.org/x/oauth2"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

const (
	syncFolderName = "synched-cloud-drives"
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
	token, err := tokenSource.Token()
	if err != nil {
		return nil, fmt.Errorf("failed to get token: %w", err)
	}

	ctx := context.Background()
	service, err := drive.NewService(ctx, option.WithTokenSource(oauth2.StaticTokenSource(token)))
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
	query := fmt.Sprintf("name='%s' and mimeType='application/vnd.google-apps.folder' and trashed=false and 'me' in owners", syncFolderName)
	
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

		for _, f := range fileList.Files {
			file := &model.File{
				ID:              f.Id,
				Name:            f.Name,
				Size:            f.Size,
				Hash:            f.Md5Checksum,
				HashAlgorithm:   "MD5",
				Provider:        model.ProviderGoogle,
				UserEmail:       c.user.Email,
				CreatedTime:     parseTime(f.CreatedTime),
				ModifiedTime:    parseTime(f.ModifiedTime),
				ParentFolderID:  folderID,
			}

			if len(f.Owners) > 0 {
				file.OwnerEmail = f.Owners[0].EmailAddress
			}

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

	_, err = io.Copy(writer, resp.Body)
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

	result := &model.File{
		ID:            createdFile.Id,
		Name:          createdFile.Name,
		Size:          createdFile.Size,
		Hash:          createdFile.Md5Checksum,
		HashAlgorithm: "MD5",
		Provider:      model.ProviderGoogle,
		UserEmail:     c.user.Email,
		CreatedTime:   parseTime(createdFile.CreatedTime),
		ModifiedTime:  parseTime(createdFile.ModifiedTime),
		ParentFolderID: folderID,
	}

	if len(createdFile.Owners) > 0 {
		result.OwnerEmail = createdFile.Owners[0].EmailAddress
	}

	return result, nil
}

// DeleteFile deletes a file
func (c *Client) DeleteFile(fileID string) error {
	return c.service.Files.Delete(fileID).Do()
}

// MoveFile moves a file to a different folder
func (c *Client) MoveFile(fileID, targetFolderID string) error {
	file, err := c.service.Files.Get(fileID).Fields("parents").Do()
	if err != nil {
		return fmt.Errorf("failed to get file: %w", err)
	}

	_, err = c.service.Files.Update(fileID, &drive.File{}).
		AddParents(targetFolderID).
		RemoveParents(file.Parents[0]).
		Do()
	
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

	file := &model.File{
		ID:            f.Id,
		Name:          f.Name,
		Size:          f.Size,
		Hash:          f.Md5Checksum,
		HashAlgorithm: "MD5",
		Provider:      model.ProviderGoogle,
		UserEmail:     c.user.Email,
		CreatedTime:   parseTime(f.CreatedTime),
		ModifiedTime:  parseTime(f.ModifiedTime),
	}

	if len(f.Parents) > 0 {
		file.ParentFolderID = f.Parents[0]
	}

	if len(f.Owners) > 0 {
		file.OwnerEmail = f.Owners[0].EmailAddress
	}

	// If no MD5 hash (e.g., Google Docs), need to download and hash
	if file.Hash == "" {
		logger.InfoTagged([]string{"Google", c.user.Email}, "No native hash for file %s, will need to calculate SHA-256", f.Name)
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

	_, err := c.service.Permissions.Create(fileID, permission).TransferOwnership(true).SendNotificationEmail(false).Do()
	if err != nil {
		return fmt.Errorf("failed to transfer ownership: %w", err)
	}

	logger.InfoTagged([]string{"Google", c.user.Email}, "Transferred ownership of file %s to %s", fileID, newOwnerEmail)
	return nil
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
