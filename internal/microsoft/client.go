package microsoft

import (
	"context"
	"fmt"
	"io"

	"cloud-drives-sync/internal/api"
	"cloud-drives-sync/internal/logger"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	msgraphsdk "github.com/microsoftgraph/msgraph-sdk-go" // CORRECTED IMPORT
	"golang.org/x/oauth2"
)

// oauth2TokenCredentialAdapter implements azcore.TokenCredential to bridge
// the gap between golang.org/x/oauth2 and the Azure SDK's authentication.
type oauth2TokenCredentialAdapter struct {
	tokenSource oauth2.TokenSource
}

// GetToken gets a token from the underlying oauth2.TokenSource.
func (a *oauth2TokenCredentialAdapter) GetToken(ctx context.Context, opts policy.TokenRequestOptions) (azcore.AccessToken, error) {
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
	client, err := msgraphsdk.NewGraphServiceClientWithCredentials(adapter, []string{}) // CORRECTED CONSTRUCTOR
	if err != nil {
		return nil, fmt.Errorf("failed to create graph service client: %w", err)
	}
	return µsoftClient{graphClient: client}, nil
}

// microsoftClient implements the api.CloudClient interface for Microsoft Graph (OneDrive).
type microsoftClient struct {
	graphClient *msgraphsdk.GraphServiceClient // CORRECTED TYPE
}

// PreflightCheck ensures the sync folder exists and is unique in the root.
func (c *microsoftClient) PreflightCheck(ctx context.Context) (string, error) {
	// Search for the folder in the drive's root.
	filter := fmt.Sprintf("name = '%s'", api.SyncFolderName)
	res, err := c.graphClient.Me().Drive().Root().Children().Get(ctx, &models.DriveItemItemChildrenRequestBuilderGetRequestConfiguration{
		QueryParameters: &models.DriveItemItemChildrenRequestBuilderGetQueryParameters{
			Filter: &filter,
		},
	})

	if err != nil {
		return "", fmt.Errorf("[Microsoft] failed to search for sync folder: %w", err)
	}

	if len(res.GetValue()) > 1 {
		return "", fmt.Errorf("[Microsoft] found %d folders named '%s' in the root. please resolve this ambiguity manually", len(res.GetValue()), api.SyncFolderName)
	}

	if len(res.GetValue()) == 1 {
		id := *res.GetValue()[0].GetId()
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
	// Need to specify which fields to select.
	requestParameters := &models.UserRequestBuilderGetQueryParameters{
		Select: []string{"userPrincipalName"},
	}
	config := &models.UserRequestBuilderGetRequestConfiguration{
		QueryParameters: requestParameters,
	}
	user, err := c.graphClient.Me().Get(ctx, config)
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

		pageIterator, err := msgraphsdk.NewPageIterator[models.DriveItemable](res, c.graphClient.GetAdapter(), models.CreateDriveItemCollectionResponseFromDiscriminatorValue)
		if err != nil {
			return fmt.Errorf("[Microsoft] failed to create page iterator for folder %s: %w", folderID, err)
		}

		err = pageIterator.Iterate(ctx, func(item models.DriveItemable) bool {
			if item.GetFolder() != nil {
				allFolders = append(allFolders, c.toApiFolderInfo(item))
				if err := list(*item.GetId()); err != nil {
					logger.TaggedError("Microsoft", "failed during recursive list: %v", err)
					return false
				}
			} else if item.GetFile() != nil {
				allFiles = append(allFiles, c.toApiFileInfo(item))
			}
			return true
		})
		return err
	}
	if err := list(rootFolderID); err != nil {
		return nil, nil, err
	}
	return allFiles, allFolders, nil
}

// UploadFile streams content to create a new file.
func (c *microsoftClient) UploadFile(ctx context.Context, parentFolderID, fileName string, fileSize int64, content io.Reader) (*api.FileInfo, error) {
	uploadSessionReq := models.NewCreateUploadSessionPostRequestBody()
	session, err := c.graphClient.Me().Drive().Items().ByDriveItemId(parentFolderID).ItemWithPath(fileName).CreateUploadSession().Post(ctx, uploadSessionReq, nil)
	if err != nil {
		return nil, fmt.Errorf("[Microsoft] failed to create upload session for '%s': %w", fileName, err)
	}

	uploader, err := msgraphsdk.NewLargeFileUploadTask[models.DriveItemable](session, c.graphClient.GetAdapter(), content, fileSize)
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
func (c *microsoftClient) DownloadFile(ctx context.Context, fileID string) (io.ReadCloser, error) {
	res, err := c.graphClient.Me().Drive().Items().ByDriveItemId(fileID).Content().Get(ctx, nil)
	if err != nil {
		return nil, err
	}
	return res, nil
}
func (c *microsoftClient) ExportFile(ctx context.Context, fileID, mimeType string) (io.ReadCloser, error) {
	var format string
	if mimeType == "application/pdf" {
		format = "pdf"
	} else {
		return nil, fmt.Errorf("[Microsoft] unsupported export format: %s", mimeType)
	}
	res, err := c.graphClient.Me().Drive().Items().ByDriveItemId(fileID).ContentWithFormat(&format).Get(ctx, nil)
	if err != nil {
		return nil, err
	}
	return res, nil
}
func (c *microsoftClient) DeleteItem(ctx context.Context, itemID string) error {
	return c.graphClient.Me().Drive().Items().ByDriveItemId(itemID).Delete(ctx, nil)
}
func (c *microsoftClient) ShareFolder(ctx context.Context, folderID, emailAddress string) error {
	req := models.NewInvitePostRequestBody()
	req.SetRequireSignIn(true)
	req.SetSendInvitation(false)
	req.SetRoles([]string{"write"})
	recipient := models.NewDriveRecipient()
	recipient.SetEmail(emailAddress)
	req.SetRecipients([]models.DriveRecipientable{recipient})
	_, err := c.graphClient.Me().Drive().Items().ByDriveItemId(folderID).Invite().Post(ctx, req, nil)
	return err
}
func (c *microsoftClient) GetStorageQuota(ctx context.Context) (*api.QuotaInfo, error) {
	drive, err := c.graphClient.Me().Drive().Get(ctx, nil)
	if err != nil {
		return nil, err
	}
	q := drive.GetQuota()
	return &api.QuotaInfo{TotalBytes: *q.GetTotal(), UsedBytes: *q.GetUsed(), FreeBytes: *q.GetRemaining()}, nil
}
func (c *microsoftClient) MoveFile(ctx context.Context, fileID, newParentFolderID, oldParentFolderID string) error {
	req := models.NewDriveItem()
	parentRef := models.NewItemReference()
	parentRef.SetId(&newParentFolderID)
	req.SetParentReference(parentRef)
	_, err := c.graphClient.Me().Drive().Items().ByDriveItemId(fileID).Patch(ctx, req, nil)
	return err
}
func (c *microsoftClient) TransferOwnership(ctx context.Context, fileID, userEmail string) (bool, error) {
	logger.TaggedInfo("Microsoft", "Native ownership transfer is not supported for OneDrive via this tool.")
	return false, nil
}
func (c *microsoftClient) toApiFileInfo(f models.DriveItemable) api.FileInfo {
	var hash, hashAlgo string
	if f.GetFile() != nil && f.GetFile().GetHashes() != nil {
		if h := f.GetFile().GetHashes().GetQuickXorHash(); h != nil {
			hash = *h
			hashAlgo = "quickXorHash"
		}
	}
	var owner string
	if f.GetCreatedBy() != nil && f.GetCreatedBy().GetUser() != nil && f.GetCreatedBy().GetUser().GetDisplayName() != nil {
		owner = *f.GetCreatedBy().GetUser().GetDisplayName()
	}
	var pID string
	if f.GetParentReference() != nil && f.GetParentReference().GetId() != nil {
		pID = *f.GetParentReference().GetId()
	}
	return api.FileInfo{ID: *f.GetId(), Name: *f.GetName(), Size: *f.GetSize(), ParentFolderIDs: []string{pID}, CreatedTime: *f.GetCreatedDateTime(), ModifiedTime: *f.GetLastModifiedDateTime(), Owner: owner, Hash: hash, HashAlgorithm: hashAlgo, IsProprietary: false, ExportLinks: make(map[string]string)}
}
func (c *microsoftClient) toApiFolderInfo(f models.DriveItemable) api.FolderInfo {
	var owner string
	if f.GetCreatedBy() != nil && f.GetCreatedBy().GetUser() != nil && f.GetCreatedBy().GetUser().GetDisplayName() != nil {
		owner = *f.GetCreatedBy().GetUser().GetDisplayName()
	}
	var pID string
	if f.GetParentReference() != nil && f.GetParentReference().GetId() != nil {
		pID = *f.GetParentReference().GetId()
	}
	return api.FolderInfo{ID: *f.GetId(), Name: *f.GetName(), ParentFolderIDs: []string{pID}, Owner: owner}
}
