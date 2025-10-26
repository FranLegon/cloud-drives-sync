package google

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"time"

	"cloud-drives-sync/internal/api"
	"cloud-drives-sync/internal/crypto"
	"cloud-drives-sync/internal/logger"
	"cloud-drives-sync/internal/model"

	"golang.org/x/oauth2"
	"google.golang.org/api/drive/v3"
	googleoauth "google.golang.org/api/oauth2/v2"
	"google.golang.org/api/option"
)

// Client implements the CloudClient interface for Google Drive
type Client struct {
	service     *drive.Service
	oauth2Svc   *googleoauth.Service
	email       string
	tokenSource oauth2.TokenSource
	log         *logger.Logger
	maxRetries  int
	retryDelay  time.Duration
}

// NewClient creates a new Google Drive client
func NewClient(ctx context.Context, tokenSource oauth2.TokenSource) (api.CloudClient, error) {
	// Create Drive service
	driveService, err := drive.NewService(ctx, option.WithTokenSource(tokenSource))
	if err != nil {
		return nil, fmt.Errorf("failed to create Drive service: %w", err)
	}

	// Create OAuth2 service for user info
	oauth2Service, err := googleoauth.NewService(ctx, option.WithTokenSource(tokenSource))
	if err != nil {
		return nil, fmt.Errorf("failed to create OAuth2 service: %w", err)
	}

	client := &Client{
		service:     driveService,
		oauth2Svc:   oauth2Service,
		tokenSource: tokenSource,
		log:         logger.New().WithPrefix("Google"),
		maxRetries:  3,
		retryDelay:  time.Second,
	}

	// Get user email
	email, err := client.GetUserEmail(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get user email: %w", err)
	}
	client.email = email
	client.log = logger.New().WithPrefix(fmt.Sprintf("Google:%s", email))

	return client, nil
}

// GetUserEmail returns the email address of the authenticated user
func (c *Client) GetUserEmail(ctx context.Context) (string, error) {
	userInfo, err := c.oauth2Svc.Userinfo.Get().Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("failed to get user info: %w", err)
	}
	return userInfo.Email, nil
}

// FindFoldersByName finds all folders with the given name
func (c *Client) FindFoldersByName(ctx context.Context, name string, includeTrash bool) ([]model.Folder, error) {
	query := fmt.Sprintf("name='%s' and mimeType='application/vnd.google-apps.folder'", name)
	if !includeTrash {
		query += " and trashed=false"
	}

	fileList, err := c.service.Files.List().
		Q(query).
		Fields("files(id, name, parents, trashed)").
		Context(ctx).
		Do()
	if err != nil {
		return nil, fmt.Errorf("failed to search for folders: %w", err)
	}

	var folders []model.Folder
	for _, file := range fileList.Files {
		parentID := ""
		if len(file.Parents) > 0 {
			parentID = file.Parents[0]
		}

		folders = append(folders, model.Folder{
			FolderID:       file.Id,
			Provider:       model.ProviderGoogle,
			OwnerEmail:     c.email,
			FolderName:     file.Name,
			ParentFolderID: parentID,
		})
	}

	return folders, nil
}

// GetOrCreateFolder gets or creates a folder with the given name and parent
func (c *Client) GetOrCreateFolder(ctx context.Context, name string, parentID string) (string, error) {
	// Search for existing folder
	query := fmt.Sprintf("name='%s' and mimeType='application/vnd.google-apps.folder' and trashed=false", name)
	if parentID != "" {
		query += fmt.Sprintf(" and '%s' in parents", parentID)
	} else {
		query += " and 'root' in parents"
	}

	fileList, err := c.service.Files.List().
		Q(query).
		Fields("files(id)").
		Context(ctx).
		Do()
	if err != nil {
		return "", fmt.Errorf("failed to search for folder: %w", err)
	}

	if len(fileList.Files) > 0 {
		return fileList.Files[0].Id, nil
	}

	// Create new folder
	folder := &drive.File{
		Name:     name,
		MimeType: "application/vnd.google-apps.folder",
	}
	if parentID != "" {
		folder.Parents = []string{parentID}
	}

	created, err := c.service.Files.Create(folder).
		Fields("id").
		Context(ctx).
		Do()
	if err != nil {
		return "", fmt.Errorf("failed to create folder: %w", err)
	}

	c.log.Info("Created folder '%s' (ID: %s)", name, created.Id)
	return created.Id, nil
}

// MoveFolder moves a folder to a new parent
func (c *Client) MoveFolder(ctx context.Context, folderID string, newParentID string) error {
	// Get current parents
	file, err := c.service.Files.Get(folderID).
		Fields("parents").
		Context(ctx).
		Do()
	if err != nil {
		return fmt.Errorf("failed to get folder: %w", err)
	}

	// Move folder by removing old parents and adding new parent
	previousParents := strings.Join(file.Parents, ",")

	if newParentID == "" {
		newParentID = "root"
	}

	_, err = c.service.Files.Update(folderID, nil).
		RemoveParents(previousParents).
		AddParents(newParentID).
		Context(ctx).
		Do()
	if err != nil {
		return fmt.Errorf("failed to move folder: %w", err)
	}

	c.log.Info("Moved folder %s to parent %s", folderID, newParentID)
	return nil
}

// ListFolders lists all folders within a parent folder
func (c *Client) ListFolders(ctx context.Context, parentID string, recursive bool) ([]model.Folder, error) {
	var allFolders []model.Folder

	if parentID == "" {
		parentID = "root"
	}

	folders, err := c.listFoldersInParent(ctx, parentID, "")
	if err != nil {
		return nil, err
	}
	allFolders = append(allFolders, folders...)

	if recursive {
		for _, folder := range folders {
			// Pass the current folder's path as prefix for subfolders
			subFolders, err := c.listFoldersInParent(ctx, folder.FolderID, folder.Path)
			if err != nil {
				return nil, err
			}
			allFolders = append(allFolders, subFolders...)

			// Recursively get sub-subfolders
			for _, subfolder := range subFolders {
				deeperFolders, err := c.ListFolders(ctx, subfolder.FolderID, true)
				if err != nil {
					return nil, err
				}
				allFolders = append(allFolders, deeperFolders...)
			}
		}
	}

	return allFolders, nil
}

// listFoldersInParent is a helper to list folders in a specific parent
func (c *Client) listFoldersInParent(ctx context.Context, parentID string, pathPrefix string) ([]model.Folder, error) {
	query := fmt.Sprintf("'%s' in parents and mimeType='application/vnd.google-apps.folder' and trashed=false", parentID)

	var folders []model.Folder
	pageToken := ""

	for {
		call := c.service.Files.List().
			Q(query).
			Fields("nextPageToken, files(id, name, parents, createdTime, modifiedTime)").
			PageSize(1000).
			Context(ctx)

		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		fileList, err := call.Do()
		if err != nil {
			return nil, fmt.Errorf("failed to list folders: %w", err)
		}

		for _, file := range fileList.Files {
			path := pathPrefix + "/" + file.Name
			normalizedPath := logger.NormalizePath(path)

			folders = append(folders, model.Folder{
				FolderID:       file.Id,
				Provider:       model.ProviderGoogle,
				OwnerEmail:     c.email,
				FolderName:     file.Name,
				ParentFolderID: parentID,
				Path:           path,
				NormalizedPath: normalizedPath,
				LastSynced:     time.Now(),
			})
		}

		pageToken = fileList.NextPageToken
		if pageToken == "" {
			break
		}
	}

	return folders, nil
}

// ListFiles lists all files within a folder
func (c *Client) ListFiles(ctx context.Context, folderID string, recursive bool) ([]model.File, error) {
	var allFiles []model.File

	if folderID == "" {
		folderID = "root"
	}

	files, err := c.listFilesInParent(ctx, folderID)
	if err != nil {
		return nil, err
	}
	allFiles = append(allFiles, files...)

	if recursive {
		folders, err := c.listFoldersInParent(ctx, folderID, "")
		if err != nil {
			return nil, err
		}

		for _, folder := range folders {
			subFiles, err := c.ListFiles(ctx, folder.FolderID, true)
			if err != nil {
				return nil, err
			}
			allFiles = append(allFiles, subFiles...)
		}
	}

	return allFiles, nil
}

// listFilesInParent is a helper to list files in a specific parent
func (c *Client) listFilesInParent(ctx context.Context, parentID string) ([]model.File, error) {
	query := fmt.Sprintf("'%s' in parents and mimeType!='application/vnd.google-apps.folder' and trashed=false", parentID)

	var files []model.File
	pageToken := ""

	for {
		call := c.service.Files.List().
			Q(query).
			Fields("nextPageToken, files(id, name, size, md5Checksum, mimeType, createdTime, modifiedTime)").
			PageSize(1000).
			Context(ctx)

		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		fileList, err := call.Do()
		if err != nil {
			return nil, fmt.Errorf("failed to list files: %w", err)
		}

		for _, file := range fileList.Files {
			hash := file.Md5Checksum
			hashAlgo := "MD5"

			// Handle Google Workspace files that don't have MD5
			if hash == "" {
				c.log.Warning("File '%s' (ID: %s) has no MD5 hash, will need fallback", file.Name, file.Id)
				// We'll compute SHA256 hash during download
				hash = "" // Will be computed later
				hashAlgo = "SHA256"
			}

			size := int64(0)
			if file.Size > 0 {
				size = file.Size
			}

			createdTime := time.Now()
			if file.CreatedTime != "" {
				if t, err := time.Parse(time.RFC3339, file.CreatedTime); err == nil {
					createdTime = t
				}
			}

			modifiedTime := time.Now()
			if file.ModifiedTime != "" {
				if t, err := time.Parse(time.RFC3339, file.ModifiedTime); err == nil {
					modifiedTime = t
				}
			}

			files = append(files, model.File{
				FileID:         file.Id,
				Provider:       model.ProviderGoogle,
				OwnerEmail:     c.email,
				FileHash:       hash,
				HashAlgorithm:  hashAlgo,
				FileName:       file.Name,
				FileSize:       size,
				ParentFolderID: parentID,
				CreatedOn:      createdTime,
				LastModified:   modifiedTime,
				LastSynced:     time.Now(),
			})
		}

		pageToken = fileList.NextPageToken
		if pageToken == "" {
			break
		}
	}

	return files, nil
}

// DownloadFile downloads a file from Google Drive
func (c *Client) DownloadFile(ctx context.Context, fileID string) (io.ReadCloser, error) {
	resp, err := c.service.Files.Get(fileID).
		Context(ctx).
		Download()
	if err != nil {
		return nil, fmt.Errorf("failed to download file: %w", err)
	}

	return resp.Body, nil
}

// UploadFile uploads a file to Google Drive
func (c *Client) UploadFile(ctx context.Context, parentFolderID string, fileName string, reader io.Reader) (*model.File, error) {
	file := &drive.File{
		Name:    fileName,
		Parents: []string{parentFolderID},
	}

	created, err := c.service.Files.Create(file).
		Media(reader).
		Fields("id, name, size, md5Checksum, createdTime, modifiedTime").
		Context(ctx).
		Do()
	if err != nil {
		return nil, fmt.Errorf("failed to upload file: %w", err)
	}

	c.log.Info("Uploaded file '%s' (ID: %s)", fileName, created.Id)

	size := int64(0)
	if created.Size > 0 {
		size = created.Size
	}

	createdTime := time.Now()
	if created.CreatedTime != "" {
		if t, err := time.Parse(time.RFC3339, created.CreatedTime); err == nil {
			createdTime = t
		}
	}

	modifiedTime := time.Now()
	if created.ModifiedTime != "" {
		if t, err := time.Parse(time.RFC3339, created.ModifiedTime); err == nil {
			modifiedTime = t
		}
	}

	return &model.File{
		FileID:         created.Id,
		Provider:       model.ProviderGoogle,
		OwnerEmail:     c.email,
		FileHash:       created.Md5Checksum,
		HashAlgorithm:  "MD5",
		FileName:       created.Name,
		FileSize:       size,
		ParentFolderID: parentFolderID,
		CreatedOn:      createdTime,
		LastModified:   modifiedTime,
		LastSynced:     time.Now(),
	}, nil
}

// DeleteFile deletes a file from Google Drive
func (c *Client) DeleteFile(ctx context.Context, fileID string) error {
	err := c.service.Files.Delete(fileID).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to delete file: %w", err)
	}

	c.log.Info("Deleted file ID: %s", fileID)
	return nil
}

// GetFileHash retrieves the hash of a file
func (c *Client) GetFileHash(ctx context.Context, fileID string) (hash string, algorithm string, err error) {
	file, err := c.service.Files.Get(fileID).
		Fields("md5Checksum, mimeType").
		Context(ctx).
		Do()
	if err != nil {
		return "", "", fmt.Errorf("failed to get file metadata: %w", err)
	}

	// If MD5 is available, use it
	if file.Md5Checksum != "" {
		return file.Md5Checksum, "MD5", nil
	}

	// For Google Workspace files, we need to export and hash
	c.log.Warning("File %s requires export for hashing (type: %s)", fileID, file.MimeType)

	// Determine export MIME type
	exportMimeType := c.getExportMimeType(file.MimeType)
	if exportMimeType == "" {
		return "", "", fmt.Errorf("cannot export file type: %s", file.MimeType)
	}

	// Download exported version
	exported, err := c.ExportFile(ctx, fileID, exportMimeType)
	if err != nil {
		return "", "", fmt.Errorf("failed to export file: %w", err)
	}
	defer exported.Close()

	// Calculate SHA256 hash
	hashValue, err := crypto.HashFile(exported)
	if err != nil {
		return "", "", fmt.Errorf("failed to hash exported file: %w", err)
	}

	return hashValue, "SHA256", nil
}

// ExportFile exports a Google Workspace file to a standard format
func (c *Client) ExportFile(ctx context.Context, fileID string, mimeType string) (io.ReadCloser, error) {
	resp, err := c.service.Files.Export(fileID, mimeType).
		Context(ctx).
		Download()
	if err != nil {
		return nil, fmt.Errorf("failed to export file: %w", err)
	}

	c.log.Info("Exported file ID: %s as %s", fileID, mimeType)
	return resp.Body, nil
}

// getExportMimeType returns the appropriate export MIME type for Google Workspace files
func (c *Client) getExportMimeType(workspaceMimeType string) string {
	exports := map[string]string{
		"application/vnd.google-apps.document":     "application/pdf",
		"application/vnd.google-apps.spreadsheet":  "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"application/vnd.google-apps.presentation": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
		"application/vnd.google-apps.drawing":      "application/pdf",
	}
	return exports[workspaceMimeType]
}

// ShareFolder shares a folder with another user
func (c *Client) ShareFolder(ctx context.Context, folderID string, targetEmail string, role string) error {
	permission := &drive.Permission{
		Type:         "user",
		Role:         role,
		EmailAddress: targetEmail,
	}

	_, err := c.service.Permissions.Create(folderID, permission).
		SendNotificationEmail(false).
		Context(ctx).
		Do()
	if err != nil {
		return fmt.Errorf("failed to share folder: %w", err)
	}

	c.log.Info("Shared folder %s with %s (role: %s)", folderID, targetEmail, role)
	return nil
}

// CheckFolderPermission checks if a user has access to a folder
func (c *Client) CheckFolderPermission(ctx context.Context, folderID string, targetEmail string) (bool, error) {
	permissions, err := c.service.Permissions.List(folderID).
		Fields("permissions(emailAddress, role)").
		Context(ctx).
		Do()
	if err != nil {
		return false, fmt.Errorf("failed to list permissions: %w", err)
	}

	for _, perm := range permissions.Permissions {
		if perm.EmailAddress == targetEmail {
			return true, nil
		}
	}

	return false, nil
}

// GetQuota retrieves storage quota information
func (c *Client) GetQuota(ctx context.Context) (*model.QuotaInfo, error) {
	about, err := c.service.About.Get().
		Fields("storageQuota").
		Context(ctx).
		Do()
	if err != nil {
		return nil, fmt.Errorf("failed to get quota: %w", err)
	}

	if about.StorageQuota == nil {
		return nil, fmt.Errorf("storage quota information not available")
	}

	total := about.StorageQuota.Limit
	used := about.StorageQuota.Usage

	percentage := 0.0
	if total > 0 {
		percentage = (float64(used) / float64(total)) * 100
	}

	return &model.QuotaInfo{
		Email:          c.email,
		Provider:       model.ProviderGoogle,
		TotalBytes:     total,
		UsedBytes:      used,
		PercentageUsed: percentage,
	}, nil
}

// TransferFileOwnership attempts to transfer file ownership (not supported in Google Drive API)
func (c *Client) TransferFileOwnership(ctx context.Context, fileID string, targetEmail string) error {
	// Google Drive API doesn't support direct ownership transfer via API
	// We'll return an error to trigger the fallback mechanism
	return fmt.Errorf("ownership transfer not supported by Google Drive API, use download/upload fallback")
}

// computeMD5 computes MD5 hash of a reader
func computeMD5(reader io.Reader) (string, error) {
	hasher := md5.New()
	if _, err := io.Copy(hasher, reader); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(hasher.Sum(nil)), nil
}
