package google

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"cloud-drives-sync/internal/api"
	"cloud-drives-sync/internal/logger"

	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

// googleClient implements the api.CloudClient interface for Google Drive.
type googleClient struct {
	service *drive.Service
}

// NewClient creates a new Google Drive client using an OAuth2-aware http.Client.
func NewClient(httpClient *http.Client) (api.CloudClient, error) {
	srv, err := drive.NewService(context.Background(), option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("failed to create google drive service: %w", err)
	}
	return &googleClient{service: srv}, nil
}

// PreflightCheck ensures the sync folder exists and is unique in the root.
func (c *googleClient) PreflightCheck(ctx context.Context) (string, error) {
	query := fmt.Sprintf("name = '%s' and 'root' in parents and mimeType = 'application/vnd.google-apps.folder' and trashed = false", api.SyncFolderName)
	files, err := c.service.Files.List().Q(query).Fields("files(id)").Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("[Google] failed to search for sync folder: %w", err)
	}

	if len(files.Files) > 1 {
		return "", fmt.Errorf("[Google] found %d folders named '%s' in the root. please resolve this ambiguity manually", len(files.Files), api.SyncFolderName)
	}

	if len(files.Files) == 1 {
		logger.TaggedInfo("Google", "Found existing sync folder with ID: %s", files.Files[0].Id)
		return files.Files[0].Id, nil
	}

	// If not found in root, create it.
	logger.TaggedInfo("Google", "Sync folder not found in root, creating a new one...")
	folderMeta := &drive.File{
		Name:     api.SyncFolderName,
		MimeType: "application/vnd.google-apps.folder",
		Parents:  []string{"root"},
	}
	createdFolder, err := c.service.Files.Create(folderMeta).Fields("id").Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("[Google] failed to create sync folder: %w", err)
	}
	logger.TaggedInfo("Google", "Successfully created sync folder with ID: %s", createdFolder.Id)
	return createdFolder.Id, nil
}

// GetUserInfo retrieves the user's email address.
func (c *googleClient) GetUserInfo(ctx context.Context) (string, error) {
	about, err := c.service.About.Get().Fields("user(emailAddress)").Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("[Google] failed to get user info: %w", err)
	}
	return about.User.EmailAddress, nil
}

// ListAllFilesAndFolders recursively scans the sync folder.
func (c *googleClient) ListAllFilesAndFolders(ctx context.Context, rootFolderID string) ([]api.FileInfo, []api.FolderInfo, error) {
	var allFiles []api.FileInfo
	var allFolders []api.FolderInfo

	var list func(folderID string) error
	list = func(folderID string) error {
		pageToken := ""
		for {
			q := fmt.Sprintf("'%s' in parents and trashed = false", folderID)
			req := c.service.Files.List().Q(q).
				Fields("nextPageToken, files(id, name, parents, createdTime, modifiedTime, size, md5Checksum, mimeType, exportLinks, owners)").
				PageSize(1000).Context(ctx)
			if pageToken != "" {
				req.PageToken(pageToken)
			}

			res, err := req.Do()
			if err != nil {
				return fmt.Errorf("[Google] failed to list items in folder %s: %w", folderID, err)
			}

			for _, f := range res.Files {
				if f.MimeType == "application/vnd.google-apps.folder" {
					allFolders = append(allFolders, c.toApiFolderInfo(f))
					if err := list(f.Id); err != nil {
						return err // Propagate recursive errors
					}
				} else {
					allFiles = append(allFiles, c.toApiFileInfo(f))
				}
			}
			pageToken = res.NextPageToken
			if pageToken == "" {
				break
			}
		}
		return nil
	}

	if err := list(rootFolderID); err != nil {
		return nil, nil, err
	}
	return allFiles, allFolders, nil
}

// CreateFolder creates a new folder.
func (c *googleClient) CreateFolder(ctx context.Context, parentFolderID, folderName string) (*api.FolderInfo, error) {
	folderMeta := &drive.File{
		Name:     folderName,
		MimeType: "application/vnd.google-apps.folder",
		Parents:  []string{parentFolderID},
	}
	f, err := c.service.Files.Create(folderMeta).Fields("id, name, parents, owners").Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	folder := c.toApiFolderInfo(f)
	return &folder, nil
}

// UploadFile streams content to create a new file.
func (c *googleClient) UploadFile(ctx context.Context, parentFolderID, fileName string, fileSize int64, content io.Reader) (*api.FileInfo, error) {
	fileMeta := &drive.File{
		Name:    fileName,
		Parents: []string{parentFolderID},
	}
	f, err := c.service.Files.Create(fileMeta).Media(content).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("[Google] failed to upload file '%s': %w", fileName, err)
	}
	info := c.toApiFileInfo(f)
	return &info, nil
}

// DownloadFile gets a reader for a standard file's content.
func (c *googleClient) DownloadFile(ctx context.Context, fileID string) (io.ReadCloser, error) {
	resp, err := c.service.Files.Get(fileID).Download(ctx)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// ExportFile gets a reader for an exported proprietary file.
func (c *googleClient) ExportFile(ctx context.Context, fileID, mimeType string) (io.ReadCloser, error) {
	resp, err := c.service.Files.Export(fileID, mimeType).Download(ctx)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// DeleteItem permanently deletes a file or folder.
func (c *googleClient) DeleteItem(ctx context.Context, itemID string) error {
	return c.service.Files.Delete(itemID).Context(ctx).Do()
}

// ShareFolder grants write/edit permissions to a user.
func (c *googleClient) ShareFolder(ctx context.Context, folderID, emailAddress string) error {
	perm := &drive.Permission{
		Type:         "user",
		Role:         "writer", // "writer" is the role for "editor" permissions.
		EmailAddress: emailAddress,
	}
	_, err := c.service.Permissions.Create(folderID, perm).Context(ctx).Do()
	return err
}

// GetStorageQuota retrieves account storage information.
func (c *googleClient) GetStorageQuota(ctx context.Context) (*api.QuotaInfo, error) {
	about, err := c.service.About.Get().Fields("storageQuota").Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	q := about.StorageQuota
	return &api.QuotaInfo{
		TotalBytes: q.Limit,
		UsedBytes:  q.Usage,
		FreeBytes:  q.Limit - q.Usage,
	}, nil
}

// MoveFile changes the parent of a file.
func (c *googleClient) MoveFile(ctx context.Context, fileID, newParentFolderID, oldParentFolderID string) error {
	_, err := c.service.Files.Update(fileID, nil).
		AddParents(newParentFolderID).
		RemoveParents(oldParentFolderID).
		Context(ctx).Do()
	return err
}

// TransferOwnership attempts to make another user the owner of a file.
func (c *googleClient) TransferOwnership(ctx context.Context, fileID, userEmail string) (bool, error) {
	perm := &drive.Permission{
		Type:         "user",
		Role:         "owner",
		EmailAddress: userEmail,
	}
	_, err := c.service.Permissions.Create(fileID, perm).TransferOwnership(true).Context(ctx).Do()
	if err != nil {
		// This operation can fail for many valid reasons (e.g., cross-domain restrictions).
		// We log it but return 'false' to indicate fallback is needed.
		logger.TaggedInfo("Google", "Native ownership transfer failed for file %s to %s: %v. Falling back.", fileID, userEmail, err)
		return false, nil
	}
	return true, nil
}

// toApiFileInfo converts a Google Drive file object to the internal API model.
func (c *googleClient) toApiFileInfo(f *drive.File) api.FileInfo {
	isProprietary := f.Md5Checksum == ""
	hashAlgo := "MD5"
	if isProprietary {
		hashAlgo = "SHA256" // Will be calculated from exported PDF/XLSX
	}

	var owner string
	if len(f.Owners) > 0 {
		owner = f.Owners[0].EmailAddress
	}

	created, _ := time.Parse(time.RFC3339, f.CreatedTime)
	modified, _ := time.Parse(time.RFC3339, f.ModifiedTime)

	return api.FileInfo{
		ID:              f.Id,
		Name:            f.Name,
		Size:            f.Size,
		ParentFolderIDs: f.Parents,
		CreatedTime:     created,
		ModifiedTime:    modified,
		Owner:           owner,
		Hash:            f.Md5Checksum,
		HashAlgorithm:   hashAlgo,
		IsProprietary:   isProprietary,
		ExportLinks:     f.ExportLinks,
	}
}

// toApiFolderInfo converts a Google Drive folder object to the internal API model.
func (c *googleClient) toApiFolderInfo(f *drive.File) api.FolderInfo {
	var owner string
	if len(f.Owners) > 0 {
		owner = f.Owners[0].EmailAddress
	}
	return api.FolderInfo{
		ID:              f.Id,
		Name:            f.Name,
		ParentFolderIDs: f.Parents,
		Owner:           owner,
	}
}
