package microsoft

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/FranLegon/cloud-drives-sync/internal/api"
	"github.com/FranLegon/cloud-drives-sync/internal/auth"
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/FranLegon/cloud-drives-sync/internal/model"
	msgraphsdk "github.com/microsoftgraph/msgraph-sdk-go"
	"github.com/microsoftgraph/msgraph-sdk-go/drives"
	"github.com/microsoftgraph/msgraph-sdk-go/models"
	"golang.org/x/oauth2"
)

const (
	syncFolderPrefix = "sync-cloud-drives"
)

// Client represents a Microsoft OneDrive client
type Client struct {
	graphClient  *msgraphsdk.GraphServiceClient
	user         *model.User
	config       *oauth2.Config
	tokenSource  *auth.TokenSource
	syncFolderID string
	driveID      string
}

// NewClient creates a new Microsoft OneDrive client
func NewClient(user *model.User, config *oauth2.Config) (*Client, error) {
	tokenSource := auth.NewTokenSource(config, user.RefreshToken)
	token, err := tokenSource.Token()
	if err != nil {
		return nil, fmt.Errorf("failed to get token: %w", err)
	}

	cred := &staticTokenCredential{token: token}
	graphClient, err := msgraphsdk.NewGraphServiceClientWithCredentials(cred, []string{})
	if err != nil {
		return nil, fmt.Errorf("failed to create graph client: %w", err)
	}

	client := &Client{
		graphClient: graphClient,
		user:        user,
		config:      config,
		tokenSource: tokenSource,
	}

	if err := client.initializeDrive(); err != nil {
		return nil, fmt.Errorf("failed to initialize drive: %w", err)
	}

	return client, nil
}

type staticTokenCredential struct {
	token *oauth2.Token
}

func (s *staticTokenCredential) GetToken(ctx context.Context, opts policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{
		Token:     s.token.AccessToken,
		ExpiresOn: s.token.Expiry,
	}, nil
}

func (c *Client) initializeDrive() error {
	ctx := context.Background()
	drive, err := c.graphClient.Me().Drive().Get(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to get drive: %w", err)
	}

	if drive.GetId() == nil {
		return fmt.Errorf("drive ID is nil")
	}

	c.driveID = *drive.GetId()
	return nil
}

// PreFlightCheck verifies the sync folder structure
func (c *Client) PreFlightCheck() error {
	ctx := context.Background()

	// List root children to find the sync folder
	result, err := c.graphClient.Drives().ByDriveId(c.driveID).Items().ByDriveItemId("root").Children().Get(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to list root: %w", err)
	}

	found := false
	for _, item := range result.GetValue() {
		if item.GetName() != nil && *item.GetName() == syncFolderPrefix && item.GetFolder() != nil {
			c.syncFolderID = *item.GetId()
			found = true
			break
		}
	}

	if !found {
		// New Requirement: If pre-flight check fails to find the folder during initialization/check,
		// it might be cleaner to just error out, unless we are in the "init/add" phase where we create it.
		// However, PreFlightCheck implies checking existing state.
		return fmt.Errorf("sync folder '%s' not found", syncFolderPrefix)
	}

	logger.InfoTagged([]string{"Microsoft", c.user.Email}, "Found sync folder '%s' (%s)", syncFolderPrefix, c.syncFolderID)
	return nil
}

// ListFiles lists files in a folder
func (c *Client) ListFiles(folderID string) ([]*model.File, error) {
	if folderID == "" {
		return nil, errors.New("folder ID is required")
	}

	ctx := context.Background()
	items, err := c.graphClient.Drives().ByDriveId(c.driveID).Items().ByDriveItemId(folderID).Children().Get(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list items: %w", err)
	}

	var allFiles []*model.File
	for _, item := range items.GetValue() {
		isFolder := item.GetFolder() != nil
		isShortcut := item.GetRemoteItem() != nil

		// If it's a shortcut, it might be a shortcut to a file or folder.
		// We only want file shortcuts here (or direct files).
		// If it's a shortcut to a folder, it should be handled in ListFolders?
		// Actually ListFolders skips non-folders.
		// Let's refine:
		// If it's a direct folder -> Skip
		// If it's a shortcut to a folder -> Skip (handled in ListFolders?)
		// If it's a file -> Process
		// If it's a shortcut to a file -> Process

		if isShortcut {
			if item.GetRemoteItem().GetFolder() != nil {
				continue // It's a folder shortcut
			}
		} else if isFolder {
			continue // It's a regular folder
		}

		modTime := time.Now()
		if item.GetLastModifiedDateTime() != nil {
			modTime = *item.GetLastModifiedDateTime()
		}

		calculatedID := fmt.Sprintf("%s-%d", *item.GetName(), *item.GetSize())

		file := &model.File{
			ID:           *item.GetId(), // Will be replaced with UUID in database layer
			Name:         *item.GetName(),
			Size:         *item.GetSize(),
			Path:         "", // Path will be set by caller
			CalculatedID: calculatedID,
			ModTime:      modTime,
			Status:       "active",
		}

		// Get hashes
		var nativeHash string
		var fileFacet models.Fileable
		if isShortcut {
			fileFacet = item.GetRemoteItem().GetFile()
		} else {
			fileFacet = item.GetFile()
		}

		if fileFacet != nil && fileFacet.GetHashes() != nil {
			hashes := fileFacet.GetHashes()
			if hashes.GetSha1Hash() != nil {
				nativeHash = *hashes.GetSha1Hash()
			}
		}

		replica := &model.Replica{
			FileID:       "", // Will be set when linking to logical file
			CalculatedID: calculatedID,
			Path:         "", // Path will be set by caller
			Name:         *item.GetName(),
			Size:         *item.GetSize(),
			Provider:     model.ProviderMicrosoft,
			AccountID:    c.user.Email,
			NativeID:     *item.GetId(),
			NativeHash:   nativeHash,
			ModTime:      modTime,
			Status:       "active",
			Fragmented:   false,
		}

		file.Replicas = []*model.Replica{replica}
		allFiles = append(allFiles, file)
	}

	return allFiles, nil
}

// DownloadFile downloads a file
func (c *Client) DownloadFile(fileID string, writer io.Writer) error {
	return fmt.Errorf("download not fully implemented for Microsoft OneDrive")
}

// UploadFile uploads a file
func (c *Client) UploadFile(folderID, name string, reader io.Reader, size int64) (*model.File, error) {
	ctx := context.Background()

	// 1. Create the file placeholder
	createRequestBody := models.NewDriveItem()
	createRequestBody.SetName(&name)
	fileFacet := models.NewFile()
	createRequestBody.SetFile(fileFacet)
	// We can't easily set conflict behavior here without config, but default is usually fail if exists.

	createdItem, err := c.graphClient.Drives().ByDriveId(c.driveID).Items().ByDriveItemId(folderID).Children().Post(ctx, createRequestBody, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create file placeholder: %w", err)
	}

	// 2. Upload content via Upload Session
	uploadSessionRequestBody := drives.NewItemItemsItemCreateUploadSessionPostRequestBody()

	uploadSession, err := c.graphClient.Drives().ByDriveId(c.driveID).Items().ByDriveItemId(*createdItem.GetId()).CreateUploadSession().Post(ctx, uploadSessionRequestBody, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create upload session: %w", err)
	}

	uploadUrl := *uploadSession.GetUploadUrl()
	chunkSize := int64(10 * 1024 * 1024) // 10MB
	buf := make([]byte, chunkSize)
	var offset int64 = 0

	for offset < size {
		remaining := size - offset
		toRead := chunkSize
		if remaining < toRead {
			toRead = remaining
		}

		n, err := io.ReadFull(reader, buf[:toRead])
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return nil, fmt.Errorf("failed to read from stream: %w", err)
		}
		if n == 0 {
			break
		}

		req, err := http.NewRequest("PUT", uploadUrl, bytes.NewReader(buf[:n]))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		req.ContentLength = int64(n)
		start := offset
		end := offset + int64(n) - 1
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to upload chunk: %w", err)
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, fmt.Errorf("chunk upload failed status %s: %s", resp.Status, string(body))
		}
		resp.Body.Close()

		offset += int64(n)
	}

	finalItem := createdItem

	modTime := time.Now()
	if finalItem.GetLastModifiedDateTime() != nil {
		modTime = *finalItem.GetLastModifiedDateTime()
	}

	calculatedID := fmt.Sprintf("%s-%d", *finalItem.GetName(), size)

	file := &model.File{
		ID:           *finalItem.GetId(), // Will be replaced with UUID in database layer
		Name:         *finalItem.GetName(),
		Size:         size,
		Path:         "", // Path will be set by caller
		CalculatedID: calculatedID,
		ModTime:      modTime,
		Status:       "active",
	}

	replica := &model.Replica{
		FileID:       "", // Will be set when linking to logical file
		CalculatedID: calculatedID,
		Path:         "", // Path will be set by caller
		Name:         *finalItem.GetName(),
		Size:         size,
		Provider:     model.ProviderMicrosoft,
		AccountID:    c.user.Email,
		NativeID:     *finalItem.GetId(),
		NativeHash:   "", // Will be populated when we get the file metadata
		ModTime:      modTime,
		Status:       "active",
		Fragmented:   false,
	}

	file.Replicas = []*model.Replica{replica}

	return file, nil
}

// UpdateFile updates file content
func (c *Client) UpdateFile(fileID string, reader io.Reader, size int64) error {
	ctx := context.Background()

	// Use Upload Session for updates
	uploadSessionRequestBody := drives.NewItemItemsItemCreateUploadSessionPostRequestBody()

	uploadSession, err := c.graphClient.Drives().ByDriveId(c.driveID).Items().ByDriveItemId(fileID).CreateUploadSession().Post(ctx, uploadSessionRequestBody, nil)
	if err != nil {
		return fmt.Errorf("failed to create upload session: %w", err)
	}

	uploadUrl := *uploadSession.GetUploadUrl()
	chunkSize := int64(10 * 1024 * 1024) // 10MB
	buf := make([]byte, chunkSize)
	var offset int64 = 0

	for offset < size {
		remaining := size - offset
		toRead := chunkSize
		if remaining < toRead {
			toRead = remaining
		}

		n, err := io.ReadFull(reader, buf[:toRead])
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return fmt.Errorf("failed to read from stream: %w", err)
		}
		if n == 0 {
			break
		}

		req, err := http.NewRequest("PUT", uploadUrl, bytes.NewReader(buf[:n]))
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}
		req.ContentLength = int64(n)
		start := offset
		end := offset + int64(n) - 1
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("failed to upload chunk: %w", err)
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return fmt.Errorf("chunk upload failed status %s: %s", resp.Status, string(body))
		}
		resp.Body.Close()

		offset += int64(n)
	}

	return nil
}

// DeleteFile deletes a file
func (c *Client) DeleteFile(fileID string) error {
	ctx := context.Background()
	return c.graphClient.Drives().ByDriveId(c.driveID).Items().ByDriveItemId(fileID).Delete(ctx, nil)
}

// MoveFile moves a file
func (c *Client) MoveFile(fileID, targetFolderID string) error {
	ctx := context.Background()
	requestBody := models.NewDriveItem()
	parentRef := models.NewItemReference()
	parentRef.SetId(&targetFolderID)
	requestBody.SetParentReference(parentRef)

	_, err := c.graphClient.Drives().ByDriveId(c.driveID).Items().ByDriveItemId(fileID).Patch(ctx, requestBody, nil)
	return err
}

// ListFolders lists folders
func (c *Client) ListFolders(parentID string) ([]*model.Folder, error) {
	if parentID == "" {
		return nil, errors.New("parent folder ID is required")
	}

	ctx := context.Background()
	items, err := c.graphClient.Drives().ByDriveId(c.driveID).Items().ByDriveItemId(parentID).Children().Get(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list items: %w", err)
	}

	var allFolders []*model.Folder
	for _, item := range items.GetValue() {
		if item.GetFolder() == nil {
			continue
		}

		folder := &model.Folder{
			ID:             *item.GetId(),
			Name:           *item.GetName(),
			Provider:       model.ProviderMicrosoft,
			UserEmail:      c.user.Email,
			ParentFolderID: parentID,
		}

		allFolders = append(allFolders, folder)
	}

	return allFolders, nil
}

// CreateFolder creates a folder
func (c *Client) CreateFolder(parentID, name string) (*model.Folder, error) {
	ctx := context.Background()

	newItem := models.NewDriveItem()
	newItem.SetName(&name)
	folderFacet := models.NewFolder()
	newItem.SetFolder(folderFacet)

	item, err := c.graphClient.Drives().ByDriveId(c.driveID).Items().ByDriveItemId(parentID).Children().Post(ctx, newItem, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create folder: %w", err)
	}

	folder := &model.Folder{
		ID:             *item.GetId(),
		Name:           *item.GetName(),
		Provider:       model.ProviderMicrosoft,
		UserEmail:      c.user.Email,
		ParentFolderID: parentID,
	}

	return folder, nil
}

// DeleteFolder deletes a folder
func (c *Client) DeleteFolder(folderID string) error {
	ctx := context.Background()
	return c.graphClient.Drives().ByDriveId(c.driveID).Items().ByDriveItemId(folderID).Delete(ctx, nil)
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

// CreateSyncFolder ensures the sync folder exists for a backup account
func (c *Client) CreateSyncFolder(name string) error {
	if name == "" {
		return fmt.Errorf("sync folder name is required")
	}

	// First check if it exists
	if err := c.PreFlightCheck(); err == nil {
		return nil // Already exists
	}

	// Create folder
	logger.InfoTagged([]string{"Microsoft", c.user.Email}, "Creating sync folder '%s'", name)
	folder, err := c.CreateFolder("root", name)
	if err != nil {
		return fmt.Errorf("failed to create sync folder: %w", err)
	}

	c.syncFolderID = folder.ID
	return nil
}

// ShareFolder shares a folder
func (c *Client) ShareFolder(folderID, email string, role string) error {
	ctx := context.Background()

	requestBody := drives.NewItemItemsItemInvitePostRequestBody()
	recipient := models.NewDriveRecipient()
	recipient.SetEmail(&email)
	requestBody.SetRecipients([]models.DriveRecipientable{recipient})

	sendInvite := false
	requestBody.SetSendInvitation(&sendInvite)

	var msRole string
	if role == "writer" {
		msRole = "write"
	} else {
		msRole = "read"
	}
	requestBody.SetRoles([]string{msRole})

	_, err := c.graphClient.Drives().ByDriveId(c.driveID).Items().ByDriveItemId(folderID).Invite().Post(ctx, requestBody, nil)
	if err != nil {
		return fmt.Errorf("failed to share item: %w", err)
	}

	logger.InfoTagged([]string{"Microsoft", c.user.Email}, "Shared item %s with %s (role: %s)", folderID, email, msRole)
	return nil
}

// VerifyPermissions verifies permissions
func (c *Client) VerifyPermissions() error {
	return nil
}

// GetQuota returns quota information
func (c *Client) GetQuota() (*api.QuotaInfo, error) {
	ctx := context.Background()
	drive, err := c.graphClient.Drives().ByDriveId(c.driveID).Get(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get drive: %w", err)
	}

	quota := &api.QuotaInfo{}
	if drive.GetQuota() != nil {
		if drive.GetQuota().GetTotal() != nil {
			quota.Total = *drive.GetQuota().GetTotal()
		}
		if drive.GetQuota().GetUsed() != nil {
			quota.Used = *drive.GetQuota().GetUsed()
		}
		if drive.GetQuota().GetRemaining() != nil {
			quota.Free = *drive.GetQuota().GetRemaining()
		}
	}

	return quota, nil
}

// GetFileMetadata retrieves file metadata
func (c *Client) GetFileMetadata(fileID string) (*model.File, error) {
	ctx := context.Background()
	item, err := c.graphClient.Drives().ByDriveId(c.driveID).Items().ByDriveItemId(fileID).Get(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get file metadata: %w", err)
	}

	modTime := time.Now()
	if item.GetLastModifiedDateTime() != nil {
		modTime = *item.GetLastModifiedDateTime()
	}

	calculatedID := fmt.Sprintf("%s-%d", *item.GetName(), *item.GetSize())

	file := &model.File{
		ID:           *item.GetId(), // Will be replaced with UUID in database layer
		Name:         *item.GetName(),
		Size:         *item.GetSize(),
		Path:         "", // Path will be set by caller
		CalculatedID: calculatedID,
		ModTime:      modTime,
		Status:       "active",
	}

	var nativeHash string
	if item.GetFile() != nil && item.GetFile().GetHashes() != nil {
		hashes := item.GetFile().GetHashes()
		if hashes.GetSha1Hash() != nil {
			nativeHash = *hashes.GetSha1Hash()
		} else if hashes.GetQuickXorHash() != nil {
			nativeHash = *hashes.GetQuickXorHash()
		}
	}

	replica := &model.Replica{
		FileID:       "", // Will be set when linking to logical file
		CalculatedID: calculatedID,
		Path:         "", // Path will be set by caller
		Name:         *item.GetName(),
		Size:         *item.GetSize(),
		Provider:     model.ProviderMicrosoft,
		AccountID:    c.user.Email,
		NativeID:     *item.GetId(),
		NativeHash:   nativeHash,
		ModTime:      modTime,
		Status:       "active",
		Fragmented:   false,
	}

	file.Replicas = []*model.Replica{replica}

	return file, nil
}

// TransferOwnership transfers ownership
func (c *Client) TransferOwnership(fileID, newOwnerEmail string) error {
	return errors.New("not implemented - OneDrive doesn't support ownership transfer")
}

// AcceptOwnership accepts ownership
func (c *Client) AcceptOwnership(fileID string) error {
	return errors.New("not implemented - OneDrive doesn't support ownership acceptance")
}

// GetUserEmail returns the user's email
func (c *Client) GetUserEmail() string {
	return c.user.Email
}

// GetUserIdentifier returns the user identifier
func (c *Client) GetUserIdentifier() string {
	return c.user.Email
}

// CreateShortcut creates a shortcut (link) to a target item
func (c *Client) CreateShortcut(parentID, name, targetID string) (*model.File, error) {
	ctx := context.Background()

	newItem := models.NewDriveItem()
	newItem.SetName(&name)

	remoteItem := models.NewRemoteItem()
	remoteItem.SetId(&targetID)
	// We assume permissions are already handled so simply pointing to ID works if accessible
	newItem.SetRemoteItem(remoteItem)

	// Post to children of parentID
	item, err := c.graphClient.Drives().ByDriveId(c.driveID).Items().ByDriveItemId(parentID).Children().Post(ctx, newItem, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create shortcut: %w", err)
	}

	size := int64(0)
	// Attempt to get remote info
	if item.GetRemoteItem() != nil && item.GetRemoteItem().GetSize() != nil {
		size = *item.GetRemoteItem().GetSize()
	}

	calculatedID := fmt.Sprintf("%s-%d", *item.GetName(), size)

	file := &model.File{
		ID:           *item.GetId(),
		Name:         *item.GetName(),
		Size:         size,
		Path:         "",
		CalculatedID: calculatedID,
		ModTime:      time.Now(),
		Status:       "active",
		Replicas:     nil, // Shortcuts don't have physical replicas
	}

	return file, nil
}
