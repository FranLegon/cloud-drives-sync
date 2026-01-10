package microsoft

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/FranLegon/cloud-drives-sync/internal/api"
	"github.com/FranLegon/cloud-drives-sync/internal/auth"
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/FranLegon/cloud-drives-sync/internal/model"
	msgraphsdk "github.com/microsoftgraph/msgraph-sdk-go"
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

		file := &model.File{
			ID:             *item.GetId(),
			Name:           *item.GetName(),
			Size:           *item.GetSize(),
			OneDriveID:     *item.GetId(),
			Provider:       model.ProviderMicrosoft,
			UserEmail:      c.user.Email,
			ParentFolderID: folderID,
		}

		if item.GetCreatedDateTime() != nil {
			file.CreatedTime = *item.GetCreatedDateTime()
		}
		if item.GetLastModifiedDateTime() != nil {
			file.ModifiedTime = *item.GetLastModifiedDateTime()
		}

		// Get hashes
		var fileFacet models.Fileable
		if isShortcut {
			fileFacet = item.GetRemoteItem().GetFile()
			// Use remote item ID as the "real" ID for cross-reference if needed,
			// but we store the shortcut ID as the main ID for operations in this account.
			// Maybe we should store RemoteID somewhere?
			// For now, let's just get the hash from the remote item.
		} else {
			fileFacet = item.GetFile()
		}

		if fileFacet != nil && fileFacet.GetHashes() != nil {
			hashes := fileFacet.GetHashes()
			if hashes.GetSha1Hash() != nil {
				file.OneDriveHash = *hashes.GetSha1Hash()
			}
		}

		file.UpdateCalculatedID()

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
	return nil, fmt.Errorf("upload not fully implemented for Microsoft OneDrive")
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
	logger.InfoTagged([]string{"Microsoft", c.user.Email}, "Would share folder %s with %s (role: %s) - not fully implemented", folderID, email, role)
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

	file := &model.File{
		ID:         *item.GetId(),
		Name:       *item.GetName(),
		Size:       *item.GetSize(),
		OneDriveID: *item.GetId(),
		Provider:   model.ProviderMicrosoft,
		UserEmail:  c.user.Email,
	}

	if item.GetCreatedDateTime() != nil {
		file.CreatedTime = *item.GetCreatedDateTime()
	}
	if item.GetLastModifiedDateTime() != nil {
		file.ModifiedTime = *item.GetLastModifiedDateTime()
	}

	if item.GetFile() != nil && item.GetFile().GetHashes() != nil {
		hashes := item.GetFile().GetHashes()
		if hashes.GetSha1Hash() != nil {
			file.OneDriveHash = *hashes.GetSha1Hash()
		}
	}

	file.UpdateCalculatedID()
	if item.GetParentReference() != nil && item.GetParentReference().GetId() != nil {
		file.ParentFolderID = *item.GetParentReference().GetId()
	}

	if item.GetFile() != nil && item.GetFile().GetHashes() != nil {
		hashes := item.GetFile().GetHashes()
		if hashes.GetQuickXorHash() != nil {
			file.OneDriveHash = *hashes.GetQuickXorHash()
		} else if hashes.GetSha1Hash() != nil {
			file.OneDriveHash = *hashes.GetSha1Hash()
		}
	}

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
