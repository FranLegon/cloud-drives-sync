package microsoft

import (
	"bytes"
	"cloud-drives-sync/internal/api"
	"cloud-drives-sync/internal/model"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/microsoftgraph/msgraph-sdk-go"
	"github.com/microsoftgraph/msgraph-sdk-go/drive/items"
	"github.com/microsoftgraph/msgraph-sdk-go/models"
	"github.com/microsoftgraph/msgraph-sdk-go/models/odataerrors"
)

// microsoftClient implements the api.CloudClient interface for Microsoft OneDrive.
type microsoftClient struct {
	graphClient *msgraph.GraphServiceClient
	ownerEmail  string
	userEmail   string
	emailOnce   sync.Once
}

// NewClient creates a new, fully functional Microsoft Graph client for OneDrive.
func NewClient(ctx context.Context, ts azcore.TokenCredential, ownerEmail string) (api.CloudClient, error) {
	authProvider, err := kiotaauthentication.NewAzureIdentityAuthenticationProviderWithScopes(ts, []string{"https://graph.microsoft.com/.default"})
	if err != nil {
		return nil, fmt.Errorf("error creating graph auth provider: %w", err)
	}
	adapter, err := msgraph.NewGraphRequestAdapter(authProvider)
	if err != nil {
		return nil, fmt.Errorf("error creating graph request adapter: %w", err)
	}
	client := msgraph.NewGraphServiceClient(adapter)
	return &microsoftClient{graphClient: client, ownerEmail: ownerEmail}, nil
}

func (c *microsoftClient) GetProviderName() string {
	return "Microsoft"
}

func (c *microsoftClient) GetUserEmail() (string, error) {
	var err error
	c.emailOnce.Do(func() {
		reqConf := &msgraph.MeRequestBuilderGetRequestConfiguration{
			QueryParameters: &msgraph.MeRequestBuilderGetQueryParameters{
				Select: []string{"userPrincipalName", "mail"},
			},
		}
		user, getErr := c.graphClient.Me().Get(context.Background(), reqConf)
		if getErr != nil {
			err = handleGraphError(getErr)
			return
		}
		if user.GetUserPrincipalName() != nil && *user.GetUserPrincipalName() != "" {
			c.userEmail = *user.GetUserPrincipalName()
		} else if user.GetMail() != nil && *user.GetMail() != "" {
			c.userEmail = *user.GetMail()
		} else {
			err = fmt.Errorf("user email not found")
		}
	})
	return c.userEmail, err
}

func (c *microsoftClient) GetAbout() (*model.StorageQuota, error) {
	drive, err := c.graphClient.Me().Drive().Get(context.Background(), nil)
	if err != nil {
		return nil, handleGraphError(err)
	}
	quota := drive.GetQuota()
	email, _ := c.GetUserEmail()
	return &model.StorageQuota{
		TotalBytes:     *quota.GetTotal(),
		UsedBytes:      *quota.GetUsed(),
		RemainingBytes: *quota.GetRemaining(),
		OwnerEmail:     email,
		Provider:       c.GetProviderName(),
	}, nil
}

func (c *microsoftClient) PreFlightCheck() (string, error) {
	filter := "name eq 'synched-cloud-drives' and folder ne null"
	reqConf := &items.ItemChildrenRequestBuilderGetRequestConfiguration{
		QueryParameters: &items.ItemChildrenRequestBuilderGetQueryParameters{
			Filter: &filter,
		},
	}
	res, err := c.graphClient.Me().Drive().Root().Children().Get(context.Background(), reqConf)
	if err != nil {
		return "", handleGraphError(err)
	}
	items := res.GetValue()
	if len(items) > 1 {
		return "", fmt.Errorf("pre-flight check failed: found %d 'synched-cloud-drives' folders in root; please resolve ambiguity", len(items))
	}
	if len(items) == 0 {
		return "", nil // Not found
	}
	return *items[0].GetId(), nil
}

func (c *microsoftClient) CreateRootSyncFolder() (string, error) {
	folder := models.NewDriveItem()
	folder.SetName("synched-cloud-drives")
	folder.SetFolder(models.NewFolder())
	folder.SetAdditionalData(map[string]interface{}{"@microsoft.graph.conflictBehavior": "fail"})
	created, err := c.graphClient.Me().Drive().Root().Children().Post(context.Background(), folder, nil)
	if err != nil {
		return "", handleGraphError(err)
	}
	return *created.GetId(), nil
}

func (c *microsoftClient) listChildren(itemID string, parentPath string, folderCallback func(model.Folder) error, fileCallback func(model.File) error) error {
	reqConf := &items.ItemChildrenRequestBuilderGetRequestConfiguration{
		QueryParameters: &items.ItemChildrenRequestBuilderGetQueryParameters{
			Select: []string{"id", "name", "size", "folder", "file", "parentReference", "createdDateTime", "lastModifiedDateTime", "hashes"},
		},
	}
	pages, err := c.graphClient.Me().Drive().Items().ByDriveItemId(itemID).Children().Get(context.Background(), reqConf)
	if err != nil {
		return handleGraphError(err)
	}

	pageIterator, err := msgraph.NewPageIterator(pages, c.graphClient.GetAdapter(), models.CreateDriveItemCollectionResponseFromDiscriminatorValue)
	if err != nil {
		return handleGraphError(err)
	}

	email, _ := c.GetUserEmail()
	err = pageIterator.Iterate(context.Background(), func(pageItem interface{}) bool {
		item := pageItem.(models.DriveItemable)
		path := parentPath + "/" + *item.GetName()
		if item.GetFolder() != nil {
			folder := model.Folder{
				FolderID:       *item.GetId(),
				Provider:       c.GetProviderName(),
				OwnerEmail:     email,
				FolderName:     *item.GetName(),
				ParentFolderID: *item.GetParentReference().GetId(),
				Path:           path,
				NormalizedPath: strings.ToLower(strings.ReplaceAll(path, "\\", "/")),
			}
			if err := folderCallback(folder); err != nil {
				return false
			}
			if err := c.listChildren(*item.GetId(), path, folderCallback, fileCallback); err != nil {
				return false
			}
		} else if item.GetFile() != nil {
			file := model.File{
				FileID:         *item.GetId(),
				Provider:       c.GetProviderName(),
				OwnerEmail:     email,
				FileName:       *item.GetName(),
				FileSize:       *item.GetSize(),
				ParentFolderID: *item.GetParentReference().GetId(),
				Path:           path,
				NormalizedPath: strings.ToLower(strings.ReplaceAll(path, "\\", "/")),
				CreatedOn:      *item.GetCreatedDateTime(),
				LastModified:   *item.GetLastModifiedDateTime(),
			}
			if item.GetFile().GetHashes() != nil && item.GetFile().GetHashes().GetQuickXorHash() != nil {
				file.FileHash = *item.GetFile().GetHashes().GetQuickXorHash()
				file.HashAlgorithm = "quickXorHash"
			} else {
				file.HashAlgorithm = "SHA256"
			}
			if err := fileCallback(file); err != nil {
				return false
			}
		}
		return true
	})
	return err
}

func (c *microsoftClient) ListFolders(folderID string, parentPath string, callback func(model.Folder) error) error {
	return c.listChildren(folderID, parentPath, callback, func(f model.File) error { return nil })
}

func (c *microsoftClient) ListFiles(folderID, parentPath string, callback func(model.File) error) error {
	return c.listChildren(folderID, parentPath, func(f model.Folder) error { return nil }, callback)
}

func (c *microsoftClient) CreateFolder(parentFolderID, name string) (*model.Folder, error) {
	folder := models.NewDriveItem()
	folder.SetName(name)
	folder.SetFolder(models.NewFolder())
	folder.SetAdditionalData(map[string]interface{}{"@microsoft.graph.conflictBehavior": "rename"})

	created, err := c.graphClient.Me().Drive().Items().ByDriveItemId(parentFolderID).Children().Post(context.Background(), folder, nil)
	if err != nil {
		return nil, handleGraphError(err)
	}
	email, _ := c.GetUserEmail()
	return &model.Folder{
		FolderID:       *created.GetId(),
		FolderName:     *created.GetName(),
		ParentFolderID: *created.GetParentReference().GetId(),
		Provider:       c.GetProviderName(),
		OwnerEmail:     email,
	}, nil
}

func (c *microsoftClient) DownloadFile(fileID string) (io.ReadCloser, int64, error) {
	item, err := c.graphClient.Me().Drive().Items().ByDriveItemId(fileID).Get(context.Background(), nil)
	if err != nil {
		return nil, 0, handleGraphError(err)
	}
	stream, err := c.graphClient.Me().Drive().Items().ByDriveItemId(fileID).Content().Get(context.Background(), nil)
	if err != nil {
		return nil, 0, handleGraphError(err)
	}
	return stream, *item.GetSize(), nil
}

func (c *microsoftClient) ExportFile(fileID, mimeType string) (io.ReadCloser, int64, error) {
	// OneDrive/SharePoint don't have proprietary formats like GSuite, so export is the same as download.
	return c.DownloadFile(fileID)
}

func (c *microsoftClient) UploadFile(parentFolderID, name string, content io.Reader, size int64) (*model.File, error) {
	if size > 4*1024*1024 {
		return c.resumableUpload(parentFolderID, name, content, size)
	}
	data, err := io.ReadAll(content)
	if err != nil {
		return nil, err
	}
	item, err := c.graphClient.Me().Drive().Items().ByDriveItemId(parentFolderID).Children().ByDriveItemId(name).Content().Put(context.Background(), data, nil)
	if err != nil {
		return nil, handleGraphError(err)
	}
	email, _ := c.GetUserEmail()
	return driveItemToFileModel(item, email, parentFolderID), nil
}

func (c *microsoftClient) resumableUpload(parentFolderID, name string, content io.Reader, size int64) (*model.File, error) {
	uploadSessionReq := items.NewItemCreateUploadSessionPostRequestBody()
	itemInfo := models.NewDriveItemUploadableProperties()
	itemInfo.SetName(name)
	conflictBehavior := "rename"
	itemInfo.SetAdditionalData(map[string]interface{}{"@microsoft.graph.conflictBehavior": &conflictBehavior})
	uploadSessionReq.SetItem(itemInfo)

	session, err := c.graphClient.Me().Drive().Items().ByDriveItemId(parentFolderID).CreateUploadSession().Post(context.Background(), uploadSessionReq, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create upload session: %w", handleGraphError(err))
	}

	uploadURL := *session.GetUploadUrl()
	chunkSize := 320 * 1024 * 10 // 3.2 MB chunks, a recommended size
	buffer := make([]byte, chunkSize)
	var bytesUploaded int64

	httpClient := http.Client{}
	for {
		bytesRead, readErr := content.Read(buffer)
		if readErr != nil && readErr != io.EOF {
			return nil, readErr
		}
		if bytesRead == 0 {
			break
		}

		req, err := http.NewRequest("PUT", uploadURL, bytes.NewReader(buffer[:bytesRead]))
		if err != nil {
			return nil, err
		}

		rangeHeader := fmt.Sprintf("bytes %d-%d/%d", bytesUploaded, bytesUploaded+int64(bytesRead)-1, size)
		req.Header.Set("Content-Length", strconv.Itoa(bytesRead))
		req.Header.Set("Content-Range", rangeHeader)

		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK { // 201 or 200 means final block was uploaded.
			var result models.DriveItem
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				return nil, err
			}
			email, _ := c.GetUserEmail()
			return driveItemToFileModel(&result, email, parentFolderID), nil
		}
		if resp.StatusCode != http.StatusAccepted { // 202 means block was received.
			return nil, fmt.Errorf("upload failed with status %s", resp.Status)
		}
		bytesUploaded += int64(bytesRead)
	}
	return nil, fmt.Errorf("upload finished but did not receive a final 200/201 status")
}

func (c *microsoftClient) DeleteFile(fileID string) error {
	_, err := c.graphClient.Me().Drive().Items().ByDriveItemId(fileID).Delete(context.Background(), nil)
	return handleGraphError(err)
}

func (c *microsoftClient) Share(folderID, emailAddress string) (string, error) {
	req := models.NewInvitePostRequestBody()
	req.SetRequireSignIn(true)
	req.SetSendInvitation(false) // Don't email the user.
	req.SetRoles([]string{"write"})
	recipient := models.NewDriveRecipient()
	recipient.SetEmail(emailAddress)
	req.SetRecipients([]models.DriveRecipientable{recipient})

	perms, err := c.graphClient.Me().Drive().Items().ByDriveItemId(folderID).Invite().Post(context.Background(), req, nil)
	if err != nil {
		return "", handleGraphError(err)
	}
	if len(perms.GetValue()) > 0 {
		return *perms.GetValue()[0].GetId(), nil
	}
	return "permission-exists", nil // Assume if no new perm is returned, it already exists.
}

func (c *microsoftClient) CheckShare(folderID, permissionID string) (bool, error) {
	_, err := c.graphClient.Me().Drive().Items().ByDriveItemId(folderID).Permissions().ByPermissionId(permissionID).Get(context.Background(), nil)
	if err != nil {
		if oerr, ok := err.(*odataerrors.ODataError); ok && oerr.GetError() != nil && *oerr.GetError().GetCode() == "itemNotFound" {
			return false, nil // 404 means permission does not exist
		}
		return false, handleGraphError(err)
	}
	return true, nil
}

func (c *microsoftClient) TransferOwnership(fileID, emailAddress string) (bool, error) {
	// Native ownership transfer is not supported by the Graph API for OneDrive in a way
	// that works between arbitrary personal and work accounts. Fallback is required.
	return false, nil
}

func (c *microsoftClient) MoveFile(fileID, currentParentID, newParentFolderID string) error {
	req := models.NewDriveItem()
	parentRef := models.NewItemReference()
	parentRef.SetId(newParentFolderID)
	req.SetParentReference(parentRef)
	_, err := c.graphClient.Me().Drive().Items().ByDriveItemId(fileID).Patch(context.Background(), req, nil)
	return handleGraphError(err)
}

// handleGraphError interprets OData errors from the Graph API for better logging and retry logic.
func handleGraphError(err error) error {
	if err == nil {
		return nil
	}
	if odataErr, ok := err.(*odataerrors.ODataError); ok && odataErr.GetError() != nil {
		code, message := "unknown", "no message"
		if odataErr.GetError().GetCode() != nil {
			code = *odataErr.GetError().GetCode()
		}
		if odataErr.GetError().GetMessage() != nil {
			message = *odataErr.GetError().GetMessage()
		}

		// Implement backoff for common transient/throttling error codes.
		if code == "activityLimitReached" || code == "throttled" || code == "serviceNotAvailable" || code == "resourceLocked" {
			time.Sleep(5 * time.Second) // Simple backoff
		}
		return fmt.Errorf("graph API error: %s - %s", code, message)
	}
	return err
}

// driveItemToFileModel is a helper to convert a Graph SDK DriveItem to our internal model.
func driveItemToFileModel(item models.DriveItemable, ownerEmail, parentID string) *model.File {
	file := &model.File{
		FileID:         *item.GetId(),
		Provider:       "Microsoft",
		OwnerEmail:     ownerEmail,
		FileName:       *item.GetName(),
		FileSize:       *item.GetSize(),
		ParentFolderID: parentID,
		CreatedOn:      *item.GetCreatedDateTime(),
		LastModified:   *item.GetLastModifiedDateTime(),
	}
	if item.GetFile() != nil && item.GetFile().GetHashes() != nil && item.GetFile().GetHashes().GetQuickXorHash() != nil {
		file.FileHash = *item.GetFile().GetHashes().GetQuickXorHash()
		file.HashAlgorithm = "quickXorHash"
	} else {
		file.HashAlgorithm = "SHA256"
	}
	return file
}
