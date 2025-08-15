package google

import (
	"cloud-drives-sync/internal/api"
	"cloud-drives-sync/internal/logger"
	"cloud-drives-sync/internal/model"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/time/rate"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// googleMimeTypeMap maps Google's proprietary document types to their standard export formats.
var googleMimeTypeMap = map[string]string{
	"application/vnd.google-apps.document":     "application/vnd.openxmlformats-officedocument.wordprocessingml.document",   // .docx
	"application/vnd.google-apps.spreadsheet":  "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",         // .xlsx
	"application/vnd.google-apps.presentation": "application/vnd.openxmlformats-officedocument.presentationml.presentation", // .pptx
	"application/vnd.google-apps.drawing":      "image/png",
}

// googleClient implements the api.CloudClient interface for Google Drive.
type googleClient struct {
	service     *drive.Service
	ownerEmail  string
	rateLimiter *rate.Limiter
	userEmail   string
	emailOnce   sync.Once
}

// NewClient creates a new Google Drive client with a built-in rate limiter
// to avoid hitting API quotas.
func NewClient(ctx context.Context, ts oauth2.TokenSource, ownerEmail string) (api.CloudClient, error) {
	httpClient := oauth2.NewClient(ctx, ts)
	service, err := drive.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("unable to retrieve Drive client: %w", err)
	}

	return &googleClient{
		service:     service,
		ownerEmail:  ownerEmail,
		rateLimiter: rate.NewLimiter(rate.Every(200*time.Millisecond), 1), // 5 requests per second
	}, nil
}

func (c *googleClient) GetProviderName() string {
	return "Google"
}

func (c *googleClient) GetUserEmail() (string, error) {
	var err error
	c.emailOnce.Do(func() {
		c.rateLimiter.Wait(context.Background())
		about, getErr := c.service.About.Get().Fields("user(emailAddress)").Do()
		if getErr != nil {
			err = handleGoogleError(getErr)
			return
		}
		c.userEmail = about.User.EmailAddress
	})
	return c.userEmail, err
}

func (c *googleClient) GetAbout() (*model.StorageQuota, error) {
	c.rateLimiter.Wait(context.Background())
	about, err := c.service.About.Get().Fields("storageQuota").Do()
	if err != nil {
		return nil, handleGoogleError(err)
	}
	sq := about.StorageQuota
	email, _ := c.GetUserEmail()
	return &model.StorageQuota{
		TotalBytes:     sq.Limit,
		UsedBytes:      sq.Usage,
		RemainingBytes: sq.Limit - sq.Usage,
		OwnerEmail:     email,
		Provider:       c.GetProviderName(),
	}, nil
}

func (c *googleClient) PreFlightCheck() (string, error) {
	c.rateLimiter.Wait(context.Background())
	query := "name = 'synched-cloud-drives' and 'me' in owners and trashed = false"
	r, err := c.service.Files.List().Q(query).Fields("files(id, parents)").Do()
	if err != nil {
		return "", handleGoogleError(err)
	}

	if len(r.Files) > 1 {
		return "", fmt.Errorf("pre-flight check failed: found %d 'synched-cloud-drives' folders; please resolve ambiguity", len(r.Files))
	}
	if len(r.Files) == 0 {
		return "", nil // Not found is a valid state, handled by the caller.
	}

	folder := r.Files[0]
	c.rateLimiter.Wait(context.Background())
	root, err := c.service.Files.Get("root").Fields("id").Do()
	if err != nil {
		return "", handleGoogleError(err)
	}

	isAtRoot := false
	for _, parentID := range folder.Parents {
		if parentID == root.Id {
			isAtRoot = true
			break
		}
	}

	if !isAtRoot {
		logger.TaggedInfo(c.ownerEmail, "Sync folder found but not at root. Moving it now.")
		c.rateLimiter.Wait(context.Background())
		_, err = c.service.Files.Update(folder.Id, nil).
			AddParents(root.Id).
			RemoveParents(strings.Join(folder.Parents, ",")).
			Do()
		if err != nil {
			return "", fmt.Errorf("failed to move 'synched-cloud-drives' to root: %w", handleGoogleError(err))
		}
	}
	return folder.Id, nil
}

func (c *googleClient) CreateRootSyncFolder() (string, error) {
	c.rateLimiter.Wait(context.Background())
	folder := &drive.File{
		Name:     "synched-cloud-drives",
		MimeType: "application/vnd.google-apps.folder",
	}
	f, err := c.service.Files.Create(folder).Fields("id").Do()
	if err != nil {
		return "", handleGoogleError(err)
	}
	return f.Id, nil
}

func (c *googleClient) ListFolders(folderID string, parentPath string, callback func(model.Folder) error) error {
	query := fmt.Sprintf("'%s' in parents and mimeType = 'application/vnd.google-apps.folder' and trashed = false", folderID)
	fields := "nextPageToken, files(id, name, parents)"

	c.rateLimiter.Wait(context.Background())
	return c.service.Files.List().Q(query).Fields(googleapi.Field(fields)).Pages(context.Background(), func(page *drive.FileList) error {
		for _, f := range page.Files {
			path := parentPath + "/" + f.Name
			email, _ := c.GetUserEmail()
			folder := model.Folder{
				FolderID:       f.Id,
				Provider:       c.GetProviderName(),
				OwnerEmail:     email,
				FolderName:     f.Name,
				ParentFolderID: f.Parents[0],
				Path:           path,
				NormalizedPath: strings.ToLower(strings.ReplaceAll(path, "\\", "/")),
			}
			if err := callback(folder); err != nil {
				return err // Stop iteration if callback returns an error
			}
			// Recurse into the subfolder
			if err := c.ListFolders(f.Id, path, callback); err != nil {
				return err
			}
		}
		c.rateLimiter.Wait(context.Background())
		return nil
	})
}

func (c *googleClient) ListFiles(folderID, parentPath string, callback func(model.File) error) error {
	query := fmt.Sprintf("'%s' in parents and mimeType != 'application/vnd.google-apps.folder' and trashed = false", folderID)
	fields := "nextPageToken, files(id, name, size, md5Checksum, mimeType, createdTime, modifiedTime, parents)"

	c.rateLimiter.Wait(context.Background())
	return c.service.Files.List().Q(query).Fields(googleapi.Field(fields)).Pages(context.Background(), func(page *drive.FileList) error {
		for _, f := range page.Files {
			path := parentPath + "/" + f.Name
			created, _ := time.Parse(time.RFC3339, f.CreatedTime)
			modified, _ := time.Parse(time.RFC3339, f.ModifiedTime)
			email, _ := c.GetUserEmail()

			file := model.File{
				FileID:         f.Id,
				Provider:       c.GetProviderName(),
				OwnerEmail:     email,
				FileName:       f.Name,
				FileSize:       f.Size,
				ParentFolderID: f.Parents[0],
				Path:           path,
				NormalizedPath: strings.ToLower(strings.ReplaceAll(path, "\\", "/")),
				CreatedOn:      created,
				LastModified:   modified,
			}

			if _, isGoogleDoc := googleMimeTypeMap[f.MimeType]; isGoogleDoc {
				file.HashAlgorithm = "SHA256" // Mark for local hashing via export
			} else if f.Md5Checksum != "" {
				file.FileHash = f.Md5Checksum
				file.HashAlgorithm = "MD5"
			} else {
				file.HashAlgorithm = "SHA256" // Fallback for binary files without MD5
			}

			if err := callback(file); err != nil {
				return err // Stop iteration
			}
		}
		c.rateLimiter.Wait(context.Background())
		return nil
	})
}

func (c *googleClient) CreateFolder(parentFolderID, name string) (*model.Folder, error) {
	c.rateLimiter.Wait(context.Background())
	folder := &drive.File{
		Name:     name,
		MimeType: "application/vnd.google-apps.folder",
		Parents:  []string{parentFolderID},
	}
	f, err := c.service.Files.Create(folder).Fields("id", "name", "parents").Do()
	if err != nil {
		return nil, handleGoogleError(err)
	}
	email, _ := c.GetUserEmail()
	return &model.Folder{
		FolderID:       f.Id,
		FolderName:     f.Name,
		ParentFolderID: f.Parents[0],
		Provider:       c.GetProviderName(),
		OwnerEmail:     email,
	}, nil
}

func (c *googleClient) DownloadFile(fileID string) (io.ReadCloser, int64, error) {
	c.rateLimiter.Wait(context.Background())
	file, err := c.service.Files.Get(fileID).Fields("size, mimeType").Do()
	if err != nil {
		return nil, 0, handleGoogleError(err)
	}
	// If it's a Google Doc, we must export it.
	if _, isGoogleDoc := googleMimeTypeMap[file.MimeType]; isGoogleDoc {
		return c.ExportFile(fileID, file.MimeType)
	}

	c.rateLimiter.Wait(context.Background())
	resp, err := c.service.Files.Get(fileID).Download()
	if err != nil {
		return nil, 0, handleGoogleError(err)
	}
	return resp.Body, file.Size, nil
}

func (c *googleClient) ExportFile(fileID, mimeType string) (io.ReadCloser, int64, error) {
	exportMimeType, ok := googleMimeTypeMap[mimeType]
	if !ok {
		return nil, 0, fmt.Errorf("no export mapping for google mime type: %s", mimeType)
	}

	c.rateLimiter.Wait(context.Background())
	resp, err := c.service.Files.Export(fileID, exportMimeType).Download()
	if err != nil {
		return nil, 0, handleGoogleError(err)
	}
	// Export does not provide a Content-Length header, so size is unknown (-1).
	return resp.Body, -1, nil
}

func (c *googleClient) UploadFile(parentFolderID, name string, content io.Reader, size int64) (*model.File, error) {
	c.rateLimiter.Wait(context.Background())
	file := &drive.File{
		Name:    name,
		Parents: []string{parentFolderID},
	}
	// Use resumable uploads for better reliability. Chunk size can be tuned.
	f, err := c.service.Files.Create(file).Media(content, googleapi.ChunkSize(8*1024*1024)).Do()
	if err != nil {
		return nil, handleGoogleError(err)
	}

	created, _ := time.Parse(time.RFC3339, f.CreatedTime)
	modified, _ := time.Parse(time.RFC3339, f.ModifiedTime)
	email, _ := c.GetUserEmail()

	return &model.File{
		FileID:         f.Id,
		Provider:       c.GetProviderName(),
		OwnerEmail:     email,
		FileName:       f.Name,
		FileSize:       f.Size,
		FileHash:       f.Md5Checksum,
		HashAlgorithm:  "MD5",
		ParentFolderID: f.Parents[0],
		CreatedOn:      created,
		LastModified:   modified,
	}, nil
}

func (c *googleClient) DeleteFile(fileID string) error {
	c.rateLimiter.Wait(context.Background())
	return handleGoogleError(c.service.Files.Delete(fileID).Do())
}

func (c *googleClient) Share(folderID, emailAddress string) (string, error) {
	c.rateLimiter.Wait(context.Background())
	perm := &drive.Permission{
		Type:         "user",
		Role:         "writer", // "writer" is the API term for "editor" role.
		EmailAddress: emailAddress,
	}
	p, err := c.service.Permissions.Create(folderID, perm).SendNotificationEmail(false).Fields("id").Do()
	if err != nil {
		// If permission already exists, API returns a 403 error. This is not a failure for us.
		if gerr, ok := err.(*googleapi.Error); ok && gerr.Code == http.StatusForbidden {
			return "permission-exists", nil // Use a special string to indicate success-if-exists
		}
		return "", handleGoogleError(err)
	}
	return p.Id, nil
}

func (c *googleClient) CheckShare(folderID, permissionID string) (bool, error) {
	c.rateLimiter.Wait(context.Background())
	_, err := c.service.Permissions.Get(folderID, permissionID).Fields("id").Do()
	if err != nil {
		if gerr, ok := err.(*googleapi.Error); ok && gerr.Code == http.StatusNotFound {
			return false, nil // 404 means the permission is missing.
		}
		return false, handleGoogleError(err)
	}
	return true, nil
}

func (c *googleClient) TransferOwnership(fileID, emailAddress string) (bool, error) {
	c.rateLimiter.Wait(context.Background())
	perm := &drive.Permission{
		Type:         "user",
		Role:         "owner",
		EmailAddress: emailAddress,
	}
	// TransferOwnership can fail for many reasons (e.g., domain policies, user not in domain).
	_, err := c.service.Permissions.Create(fileID, perm).TransferOwnership(true).Do()
	if err != nil {
		// Log the reason but return false instead of an error, as this is an expected outcome.
		logger.Warn("google", err, "native ownership transfer failed")
		return false, nil
	}
	return true, nil
}

func (c *googleClient) MoveFile(fileID, currentParentID, newParentFolderID string) error {
	c.rateLimiter.Wait(context.Background())
	_, err := c.service.Files.Update(fileID, nil).
		AddParents(newParentFolderID).
		RemoveParents(currentParentID).
		Do()
	return handleGoogleError(err)
}

// handleGoogleError checks for rate limit or server-side errors which are retryable.
func handleGoogleError(err error) error {
	if err == nil {
		return nil
	}
	if gerr, ok := err.(*googleapi.Error); ok {
		// 403 can be rate limiting or other permission issues. 429 is definitely rate limiting.
		if gerr.Code == 403 || gerr.Code == 429 || gerr.Code >= 500 {
			// A simple retry strategy. Production code might use exponential backoff.
			time.Sleep(2 * time.Second)
		}
	}
	return err
}
