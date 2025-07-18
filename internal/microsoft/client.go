package microsoft

import (
	"context"
	"fmt"
	"io"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/microsoftgraph/msgraph-sdk-go/graph"
	"github.com/microsoftgraph/msgraph-sdk-go/models"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/api"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/logger"
	"golang.org/x/oauth2"
)

// oauth2TokenCredentialAdapter implements azcore.TokenCredential to bridge
// the gap between golang.org/x/oauth2 and the Azure SDK's authentication.
type oauth2TokenCredentialAdapter struct {
	tokenSource oauth2.TokenSource
}

// GetToken gets a token from the underlying oauth2.TokenSource.
func (a *oauth2TokenCredentialAdapter) GetToken(ctx context.Context, opts policy.TokenRequestOptions) (azcore.AccessToken, error) {
	// Note: opts.Scopes are ignored; we use the scopes from the initial OAuth config.
	token, err := a.tokenSource.Token()
	if err != nil {
		return azcore.AccessToken{}, err
	}
	return azcore.AccessToken{Token: token.AccessToken, ExpiresOn: token.Expiry}, nil
}

// NewClient creates a new Microsoft Graph client from an oauth2.TokenSource.
func NewClient(ts oauth2.TokenSource) (api.CloudClient, error) {
	adapter := &oauth2TokenCredentialAdapter{tokenSource: ts}
	// The scopes are effectively set by the token source, not here.
	client, err := graph.NewGraphServiceClientWithCredentials(adapter, []string{})
	if err != nil {
		return nil, fmt.Errorf("failed to create graph service client: %w", err)
	}
	return µsoftClient{graphClient: client}, nil
}

// microsoftClient implements the api.CloudClient interface for Microsoft Graph (OneDrive).
type microsoftClient struct {
	graphClient *graph.GraphServiceClient
}

// PreflightCheck ensures the sync folder exists and is unique in the root.
func (c *microsoftClient) PreflightCheck(ctx context.Context) (string, error) {
	// Search for the folder in the drive's root.
	res, err := c.graphClient.Me().Drive().Root().SearchWithQ(api.SyncFolderName).Get(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("[Microsoft] failed to search for sync folder: %w", err)
	}

	rootItems := make([]models.DriveItemable, 0)
	for _, item := range res.GetValue() {
		if item.GetParentReference() != nil && item.GetParentReference().GetPath() != nil && *item.GetParentReference().GetPath() == "/drive/root:" {
			rootItems = append(rootItems, item)
		}
	}

	if len(rootItems) > 1 {
		return "", fmt.Errorf("[Microsoft] found %d folders named '%s' in the root. please resolve this ambiguity manually", len(rootItems), api.SyncFolderName)
	}

	if len(rootItems) == 1 {
		id := *rootItems[0].GetId()
		logger.TaggedInfo("Microsoft", "Found existing sync folder with ID: %s", id)
		return id, nil
	}

	// If not found, create it.
	logger.TaggedInfo("Microsoft", "Sync folder not found in root, creating a new one...")
	folderReq := models.NewDriveItem()
	folderReq.SetName(api.SyncFolderName)
	folderReq.SetFolder(models.NewFolder())
	createdFolder, err := c.graphClient.Me().Drive().Root().Children().Post(ctx, folderReq, nil)
	if err != nil {
		return "", fmt.Errorf("[Microsoft] failed to create sync folder: %w", err)
	}
	id := *createdFolder.GetId()
	logger.TaggedInfo("Microsoft", "Successfully created sync folder with ID: %s", id)
	return id, nil
}

// GetUserInfo retrieves the user's principal name (usually their email).
func (c *microsoftClient) GetUserInfo(ctx context.Context) (string, error) {
	user, err := c.graphClient.Me().Get(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("[Microsoft] failed to get user info: %w", err)
	}
	return *user.GetUserPrincipalName(), nil
}

// ListAllFilesAndFolders recursively scans the sync folder.
func (c *microsoftClient) ListAllFilesAndFolders(ctx context.Context, rootFolderID string) ([]api.FileInfo, []api.FolderInfo, error) {
	var allFiles []api.FileInfo
	var allFolders []api.FolderInfo

	var list func(folderID string) error
	list = func(folderID string) error {
		res, err := c.graphClient.Me().Drive().Items().ByDriveItemId(folderID).Children().Get(ctx, nil)
		if err != nil {
			return fmt.Errorf("[Microsoft] failed to list items in folder %s: %w", folderID, err)
		}

		pageIterator, err := graph.NewPageIterator[models.DriveItemable](res, c.graphClient.GetAdapter(), models.CreateDriveItemCollectionResponseFromDiscriminatorValue)
		if err != nil {
			return fmt.Errorf("[Microsoft] failed to create page iterator for folder %s: %w", folderID, err)
		}

		err = pageIterator.Iterate(ctx, func(item models.DriveItemable) bool {
			if item.GetFolder() != nil {
				allFolders = append(allFolders, c.toApiFolderInfo(item))
				// Recurse, but stop iteration if an error occurs.
				if err := list(*item.GetId()); err != nil {
					logger.TaggedError("Microsoft", "failed during recursive list: %v", err)
					return false
				}
			} else if item.GetFile() != nil {
				allFiles = append(allFiles, c.toApiFileInfo(item))
			}
			return true // Continue iteration
		})
		return err
	}
	if err := list(rootFolderID); err != nil {
		return nil, nil, err
	}
	return allFiles, allFolders, nil
}

// CreateFolder creates a new folder.
func (c *microsoftClient) CreateFolder(ctx context.Context, parentFolderID, folderName string) (*api.FolderInfo, error) {
	req := models.NewDriveItem()
	req.SetName(folderName)
	req.SetFolder(models.NewFolder())

	f, err := c.graphClient.Me().Drive().Items().ByDriveItemId(parentFolderID).Children().Post(ctx, req, nil)
	if err != nil {
		return nil, fmt.Errorf("[Microsoft] failed to create folder '%s': %w", folderName, err)
	}
	folder := c.toApiFolderInfo(f)
	return &folder, nil
}

// UploadFile streams content to create a new file.
func (c *microsoftClient) UploadFile(ctx context.Context, parentFolderID, fileName string, fileSize int64, content io.Reader) (*api.FileInfo, error) {
	// For files > 4MB, an upload session is required. For simplicity and robustness, we use it for all files.
	uploadSessionReq := models.NewCreateUploadSessionPostRequestBody()
	session, err := c.graphClient.Me().Drive().Items().ByDriveItemId(parentFolderID).ItemWithPath(fileName).CreateUploadSession().Post(ctx, uploadSessionReq, nil)
	if err != nil {
		return nil, fmt.Errorf("[Microsoft] failed to create upload session for '%s': %w", fileName, err)
	}

	uploader, err := graph.NewLargeFileUploadTask[models.DriveItemable](session, c.graphClient.GetAdapter(), content, fileSize)
	if err != nil {
		return nil, fmt.Errorf("[Microsoft] failed to create upload task for '%s': %w", fileName, err)
	}

	res, err := uploader.Upload(ctx)
	if err != nil {
		return nil, fmt.Errorf("[Microsoft] upload failed for '%s': %w", fileName, err)
	}

	info := c.toApiFileInfo(res.GetUploadResult())
	return &info, nil
}

// DownloadFile gets a reader for a file's content.
func (c *microsoftClient) DownloadFile(ctx context.Context, fileID string) (io.ReadCloser, error) {
	return c.graphClient.Me().Drive().Items().ByDriveItemId(fileID).Content().Get(ctx, nil)
}

// ExportFile gets a reader for an exported file (e.g., PDF from DOCX).
func (c *microsoftClient) ExportFile(ctx context.Context, fileID, mimeType string) (io.ReadCloser, error) {
	var format string
	switch mimeType {
	case "application/pdf":
		format = "pdf"
	default:
		return nil, fmt.Errorf("[Microsoft] unsupported export format: %s", mimeType)
	}
	return c.graphClient.Me().Drive().Items().ByDriveItemId(fileID).ContentWithFormat(&format).Get(ctx, nil)
}

// DeleteItem permanently deletes a file or folder.
func (c *microsoftClient) DeleteItem(ctx context.Context, itemID string) error {
	return c.graphClient.Me().Drive().Items().ByDriveItemId(itemID).Delete(ctx, nil)
}

// ShareFolder grants write permissions to a user.
func (c *microsoftClient) ShareFolder(ctx context.Context, folderID, emailAddress string) error {
	req := models.NewInvitePostRequestBody()
	req.SetRequireSignIn(true)
	req.SetSendInvitation(false)    // Don't send an email from Graph
	req.SetRoles([]string{"write"}) // "write" is the role for "editor"

	recipient := models.NewDriveRecipient()
	recipient.SetEmail(emailAddress)
	req.SetRecipients([]models.DriveRecipientable{recipient})

	_, err := c.graphClient.Me().Drive().Items().ByDriveItemId(folderID).Invite().Post(ctx, req, nil)
	return err
}

// GetStorageQuota retrieves account storage information.
func (c *microsoftClient) GetStorageQuota(ctx context.Context) (*api.QuotaInfo, error) {
	drive, err := c.graphClient.Me().Drive().Get(ctx, nil)
	if err != nil {
		return nil, err
	}
	q := drive.GetQuota()
	return &api.QuotaInfo{
		TotalBytes: *q.GetTotal(),
		UsedBytes:  *q.GetUsed(),
		FreeBytes:  *q.GetRemaining(),
	}, nil
}

// MoveFile changes the parent of a file.
func (c *microsoftClient) MoveFile(ctx context.Context, fileID, newParentFolderID, oldParentFolderID string) error {
	req := models.NewDriveItem()
	parentRef := models.NewItemReference()
	parentRef.SetId(&newParentFolderID)
	req.SetParentReference(parentRef)
	_, err := c.graphClient.Me().Drive().Items().ByDriveItemId(fileID).Patch(ctx, req, nil)
	return err
}

// TransferOwnership for OneDrive is not supported through this API for typical user accounts.
func (c *microsoftClient) TransferOwnership(ctx context.Context, fileID, userEmail string) (bool, error) {
	logger.TaggedInfo("Microsoft", "Native ownership transfer is not supported for OneDrive via this tool.")
	return false, nil // Indicate fallback is required.
}

// toApiFileInfo converts a Graph DriveItem to the internal API model.
func (c *microsoftClient) toApiFileInfo(f models.DriveItemable) api.FileInfo {
	var hash, hashAlgo string
	if f.GetFile() != nil && f.GetFile().GetHashes() != nil {
		if h := f.GetFile().GetHashes().GetQuickXorHash(); h != nil {
			hash = *h
			hashAlgo = "quickXorHash"
		}
	}
	var owner string
	if f.GetCreatedBy() != nil && f.GetCreatedBy().GetUser() != nil {
		owner = *f.GetCreatedBy().GetUser().GetDisplayName()
	}

	return api.FileInfo{
		ID:              *f.GetId(),
		Name:            *f.GetName(),
		Size:            *f.GetSize(),
		ParentFolderIDs: []string{*f.GetParentReference().GetId()},
		CreatedTime:     *f.GetCreatedDateTime(),
		ModifiedTime:    *f.GetLastModifiedDateTime(),
		Owner:           owner,
		Hash:            hash,
		HashAlgorithm:   hashAlgo,
		IsProprietary:   false, // Graph API treats Office docs as regular files for download
		ExportLinks:     make(map[string]string),
	}
}

// toApiFolderInfo converts a Graph DriveItem folder to the internal API model.
func (c *microsoftClient) toApiFolderInfo(f models.DriveItemable) api.FolderInfo {
	var owner string
	if f.GetCreatedBy() != nil && f.GetCreatedBy().GetUser() != nil {
		owner = *f.GetCreatedBy().GetUser().GetDisplayName()
	}
	return api.FolderInfo{
		ID:              *f.GetId(),
		Name:            *f.GetName(),
		ParentFolderIDs: []string{*f.GetParentReference().GetId()},
		Owner:           owner,
	}
}
