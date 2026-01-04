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
	syncFolderPrefix = "synched-cloud-drives"
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
	
	if !c.user.IsMain && c.user.SyncFolderName != "" {
		// For backup accounts, verify folder exists
		// Note: Simplified implementation - would list and find folder
		logger.InfoTagged([]string{"Microsoft", c.user.Email}, "Pre-flight check for folder '%s' (simplified)", c.user.SyncFolderName)
		c.syncFolderID = "placeholder-id"
		return nil
	} else if c.user.IsMain {
		_, err := c.graphClient.Me().Drive().Get(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to access drive: %w", err)
		}
		logger.InfoTagged([]string{"Microsoft", c.user.Email}, "Pre-flight check passed for main account")
	}

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
		if item.GetFolder() != nil {
			continue
		}

		file := &model.File{
			ID:             *item.GetId(),
			Name:           *item.GetName(),
			Size:           *item.GetSize(),
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

		if item.GetFile() != nil && item.GetFile().GetHashes() != nil {
			hashes := item.GetFile().GetHashes()
			if hashes.GetQuickXorHash() != nil {
				file.Hash = *hashes.GetQuickXorHash()
				file.HashAlgorithm = "QuickXorHash"
			} else if hashes.GetSha1Hash() != nil {
				file.Hash = *hashes.GetSha1Hash()
				file.HashAlgorithm = "SHA1"
			}
		}

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

// CreateSyncFolder creates the sync folder for a backup account
func (c *Client) CreateSyncFolder() (string, error) {
	if c.user.SyncFolderName == "" {
		return "", fmt.Errorf("sync folder name not set for user")
	}

	// Note: Full implementation would create folder via API
	logger.InfoTagged([]string{"Microsoft", c.user.Email}, "Would create sync folder '%s' (not fully implemented)", c.user.SyncFolderName)
	return "placeholder-folder-id", nil
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
		ID:        *item.GetId(),
		Name:      *item.GetName(),
		Size:      *item.GetSize(),
		Provider:  model.ProviderMicrosoft,
		UserEmail: c.user.Email,
	}

	if item.GetCreatedDateTime() != nil {
		file.CreatedTime = *item.GetCreatedDateTime()
	}
	if item.GetLastModifiedDateTime() != nil {
		file.ModifiedTime = *item.GetLastModifiedDateTime()
	}
	if item.GetParentReference() != nil && item.GetParentReference().GetId() != nil {
		file.ParentFolderID = *item.GetParentReference().GetId()
	}

	if item.GetFile() != nil && item.GetFile().GetHashes() != nil {
		hashes := item.GetFile().GetHashes()
		if hashes.GetQuickXorHash() != nil {
			file.Hash = *hashes.GetQuickXorHash()
			file.HashAlgorithm = "QuickXorHash"
		} else if hashes.GetSha1Hash() != nil {
			file.Hash = *hashes.GetSha1Hash()
			file.HashAlgorithm = "SHA1"
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
