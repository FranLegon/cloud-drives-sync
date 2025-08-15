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

	"golang.org/x/oauth2"
)

const graphAPIEndpoint = "https://graph.microsoft.com/v1.0"

// --- Structs for Unmarshalling Graph API JSON Responses ---

type graphUser struct {
	UserPrincipalName string `json:"userPrincipalName"`
	Mail              string `json:"mail"`
}

type graphDrive struct {
	Quota struct {
		Total     int64 `json:"total"`
		Used      int64 `json:"used"`
		Remaining int64 `json:"remaining"`
	} `json:"quota"`
}

type graphDriveItem struct {
	ID                   string    `json:"id"`
	Name                 string    `json:"name"`
	Size                 int64     `json:"size"`
	CreatedDateTime      time.Time `json:"createdDateTime"`
	LastModifiedDateTime time.Time `json:"lastModifiedDateTime"`
	ParentReference      struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	} `json:"parentReference"`
	File   *struct{} `json:"file"`
	Folder *struct{} `json:"folder"`
	Hashes *struct {
		QuickXorHash string `json:"quickXorHash"`
	} `json:"hashes"`
}

type graphDriveItemCollection struct {
	Value    []graphDriveItem `json:"value"`
	NextLink string           `json:"@odata.nextLink"`
}

type graphErrorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type graphUploadSession struct {
	UploadURL string `json:"uploadUrl"`
}

type graphPermission struct {
	ID string `json:"id"`
}

// --- Client Implementation ---

// microsoftClient implements api.CloudClient using direct HTTP requests.
type microsoftClient struct {
	httpClient *http.Client
	ownerEmail string
	userEmail  string
	emailOnce  sync.Once
}

// NewClient creates a new Microsoft client that uses the standard library http.Client.
func NewClient(ctx context.Context, ts oauth2.TokenSource, ownerEmail string) (api.CloudClient, error) {
	httpClient := oauth2.NewClient(ctx, ts)
	return &microsoftClient{
		httpClient: httpClient,
		ownerEmail: ownerEmail,
	}, nil
}

func (c *microsoftClient) GetProviderName() string {
	return "Microsoft"
}

func (c *microsoftClient) GetUserEmail() (string, error) {
	var err error
	c.emailOnce.Do(func() {
		var user graphUser
		err = c.doRequest("GET", graphAPIEndpoint+"/me?$select=userPrincipalName,mail", nil, &user)
		if err != nil {
			return
		}
		if user.UserPrincipalName != "" {
			c.userEmail = user.UserPrincipalName
		} else if user.Mail != "" {
			c.userEmail = user.Mail
		} else {
			err = fmt.Errorf("user email not found in API response")
		}
	})
	return c.userEmail, err
}

func (c *microsoftClient) GetAbout() (*model.StorageQuota, error) {
	var drive graphDrive
	if err := c.doRequest("GET", graphAPIEndpoint+"/me/drive", nil, &drive); err != nil {
		return nil, err
	}
	email, _ := c.GetUserEmail()
	return &model.StorageQuota{
		TotalBytes:     drive.Quota.Total,
		UsedBytes:      drive.Quota.Used,
		RemainingBytes: drive.Quota.Remaining,
		OwnerEmail:     email,
		Provider:       c.GetProviderName(),
	}, nil
}

func (c *microsoftClient) PreFlightCheck() (string, error) {
	url := fmt.Sprintf("%s/me/drive/root/search(q='%s')", graphAPIEndpoint, "synched-cloud-drives")
	var collection graphDriveItemCollection
	if err := c.doRequest("GET", url, nil, &collection); err != nil {
		return "", err
	}

	var foundFolders []graphDriveItem
	for _, item := range collection.Value {
		if item.Folder != nil && item.ParentReference.Path != "" && strings.HasSuffix(item.ParentReference.Path, "/drive/root:") {
			foundFolders = append(foundFolders, item)
		}
	}

	if len(foundFolders) > 1 {
		return "", fmt.Errorf("found %d 'synched-cloud-drives' folders in root; please resolve ambiguity", len(foundFolders))
	}
	if len(foundFolders) == 0 {
		return "", nil // Not found
	}
	return foundFolders[0].ID, nil
}

func (c *microsoftClient) CreateRootSyncFolder() (string, error) {
	body := map[string]interface{}{
		"name":                              "synched-cloud-drives",
		"folder":                            map[string]interface{}{},
		"@microsoft.graph.conflictBehavior": "fail",
	}
	var createdItem graphDriveItem
	if err := c.doRequest("POST", graphAPIEndpoint+"/me/drive/root/children", body, &createdItem); err != nil {
		return "", err
	}
	return createdItem.ID, nil
}

func (c *microsoftClient) listChildren(itemID string, parentPath string, folderCallback func(model.Folder) error, fileCallback func(model.File) error) error {
	nextLink := fmt.Sprintf("%s/me/drive/items/%s/children?$select=id,name,size,folder,file,parentReference,createdDateTime,lastModifiedDateTime,hashes", graphAPIEndpoint, itemID)
	email, _ := c.GetUserEmail()

	for nextLink != "" {
		var collection graphDriveItemCollection
		if err := c.doRequest("GET", nextLink, nil, &collection); err != nil {
			return err
		}

		for _, item := range collection.Value {
			path := parentPath + "/" + item.Name
			if item.Folder != nil {
				folder := model.Folder{
					FolderID:       item.ID,
					Provider:       c.GetProviderName(),
					OwnerEmail:     email,
					FolderName:     item.Name,
					ParentFolderID: item.ParentReference.ID,
					Path:           path,
					NormalizedPath: strings.ToLower(strings.ReplaceAll(path, "\\", "/")),
				}
				if err := folderCallback(folder); err != nil {
					return err
				}
				if err := c.listChildren(item.ID, path, folderCallback, fileCallback); err != nil {
					return err
				}
			} else if item.File != nil {
				file := model.File{
					FileID:         item.ID,
					Provider:       c.GetProviderName(),
					OwnerEmail:     email,
					FileName:       item.Name,
					FileSize:       item.Size,
					ParentFolderID: item.ParentReference.ID,
					Path:           path,
					NormalizedPath: strings.ToLower(strings.ReplaceAll(path, "\\", "/")),
					CreatedOn:      item.CreatedDateTime,
					LastModified:   item.LastModifiedDateTime,
				}
				if item.Hashes != nil {
					file.FileHash = item.Hashes.QuickXorHash
					file.HashAlgorithm = "quickXorHash"
				} else {
					file.HashAlgorithm = "SHA256"
				}
				if err := fileCallback(file); err != nil {
					return err
				}
			}
		}
		nextLink = collection.NextLink
	}
	return nil
}

func (c *microsoftClient) ListFolders(folderID string, parentPath string, callback func(model.Folder) error) error {
	return c.listChildren(folderID, parentPath, callback, func(f model.File) error { return nil })
}

func (c *microsoftClient) ListFiles(folderID, parentPath string, callback func(model.File) error) error {
	return c.listChildren(folderID, parentPath, func(f model.Folder) error { return nil }, callback)
}

func (c *microsoftClient) CreateFolder(parentFolderID, name string) (*model.Folder, error) {
	url := fmt.Sprintf("%s/me/drive/items/%s/children", graphAPIEndpoint, parentFolderID)
	body := map[string]interface{}{
		"name":                              name,
		"folder":                            map[string]interface{}{},
		"@microsoft.graph.conflictBehavior": "rename",
	}
	var createdItem graphDriveItem
	if err := c.doRequest("POST", url, body, &createdItem); err != nil {
		return nil, err
	}
	email, _ := c.GetUserEmail()
	return &model.Folder{
		FolderID:       createdItem.ID,
		FolderName:     createdItem.Name,
		ParentFolderID: createdItem.ParentReference.ID,
		Provider:       c.GetProviderName(),
		OwnerEmail:     email,
	}, nil
}

func (c *microsoftClient) DownloadFile(fileID string) (io.ReadCloser, int64, error) {
	url := fmt.Sprintf("%s/me/drive/items/%s/content", graphAPIEndpoint, fileID)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, 0, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, 0, handleGraphErrorResponse(resp)
	}
	return resp.Body, resp.ContentLength, nil
}

func (c *microsoftClient) ExportFile(fileID, mimeType string) (io.ReadCloser, int64, error) {
	return c.DownloadFile(fileID)
}

func (c *microsoftClient) UploadFile(parentFolderID, name string, content io.Reader, size int64) (*model.File, error) {
	if size > 4*1024*1024 {
		return c.resumableUpload(parentFolderID, name, content, size)
	}
	url := fmt.Sprintf("%s/me/drive/items/%s:/%s:/content", graphAPIEndpoint, parentFolderID, name)
	req, err := http.NewRequest("PUT", url, content)
	if err != nil {
		return nil, err
	}

	var uploadedItem graphDriveItem
	err = c.doRequestWithReq(req, &uploadedItem)
	if err != nil {
		return nil, err
	}

	email, _ := c.GetUserEmail()
	return driveItemToFileModel(&uploadedItem, email, parentFolderID), nil
}

func (c *microsoftClient) resumableUpload(parentFolderID, name string, content io.Reader, size int64) (*model.File, error) {
	// 1. Create upload session
	url := fmt.Sprintf("%s/me/drive/items/%s:/%s:/createUploadSession", graphAPIEndpoint, parentFolderID, name)
	var session graphUploadSession
	if err := c.doRequest("POST", url, map[string]interface{}{}, &session); err != nil {
		return nil, fmt.Errorf("failed to create upload session: %w", err)
	}

	// 2. Upload in chunks
	chunkSize := 320 * 1024 * 8 // 2.5MB chunks
	buffer := make([]byte, chunkSize)
	var bytesUploaded int64

	for {
		bytesRead, readErr := content.Read(buffer)
		if readErr != nil && readErr != io.EOF {
			return nil, readErr
		}
		if bytesRead == 0 {
			break
		}

		req, err := http.NewRequest("PUT", session.UploadURL, bytes.NewReader(buffer[:bytesRead]))
		if err != nil {
			return nil, err
		}

		rangeHeader := fmt.Sprintf("bytes %d-%d/%d", bytesUploaded, bytesUploaded+int64(bytesRead)-1, size)
		req.Header.Set("Content-Length", strconv.Itoa(bytesRead))
		req.Header.Set("Content-Range", rangeHeader)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK {
			var result graphDriveItem
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				return nil, err
			}
			email, _ := c.GetUserEmail()
			return driveItemToFileModel(&result, email, parentFolderID), nil
		}
		if resp.StatusCode != http.StatusAccepted {
			return nil, handleGraphErrorResponse(resp)
		}
		bytesUploaded += int64(bytesRead)
	}
	return nil, fmt.Errorf("upload finished but did not receive a final 200/201 status")
}

func (c *microsoftClient) DeleteFile(fileID string) error {
	url := fmt.Sprintf("%s/me/drive/items/%s", graphAPIEndpoint, fileID)
	return c.doRequest("DELETE", url, nil, nil)
}

func (c *microsoftClient) Share(folderID, emailAddress string) (string, error) {
	url := fmt.Sprintf("%s/me/drive/items/%s/invite", graphAPIEndpoint, folderID)
	body := map[string]interface{}{
		"recipients": []map[string]string{
			{"email": emailAddress},
		},
		"requireSignIn":  true,
		"sendInvitation": false,
		"roles":          []string{"write"},
	}
	var perms struct {
		Value []graphPermission `json:"value"`
	}
	err := c.doRequest("POST", url, body, &perms)
	if err != nil {
		if strings.Contains(err.Error(), "The recipient is already a member") {
			return "permission-exists", nil
		}
		return "", err
	}
	if len(perms.Value) > 0 {
		return perms.Value[0].ID, nil
	}
	return "permission-unknown", nil
}

func (c *microsoftClient) CheckShare(folderID, permissionID string) (bool, error) {
	url := fmt.Sprintf("%s/me/drive/items/%s/permissions/%s", graphAPIEndpoint, folderID, permissionID)
	err := c.doRequest("GET", url, nil, nil)
	if err != nil {
		if strings.Contains(err.Error(), "itemNotFound") {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (c *microsoftClient) TransferOwnership(fileID, emailAddress string) (bool, error) {
	return false, nil // Not supported via simple API calls
}

func (c *microsoftClient) MoveFile(fileID, currentParentID, newParentFolderID string) error {
	url := fmt.Sprintf("%s/me/drive/items/%s", graphAPIEndpoint, fileID)
	body := map[string]interface{}{
		"parentReference": map[string]string{
			"id": newParentFolderID,
		},
	}
	return c.doRequest("PATCH", url, body, nil)
}

// --- HTTP Helpers ---

func (c *microsoftClient) doRequest(method, url string, body interface{}, result interface{}) error {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		return err
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return c.doRequestWithReq(req, result)
}

func (c *microsoftClient) doRequestWithReq(req *http.Request, result interface{}) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return handleGraphErrorResponse(resp)
	}

	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return err
		}
	}
	return nil
}

func handleGraphErrorResponse(resp *http.Response) error {
	var errResp graphErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&errResp); err != nil {
		return fmt.Errorf("HTTP %s", resp.Status)
	}
	code := errResp.Error.Code
	if code == "" {
		code = "unknown"
	}
	message := errResp.Error.Message
	if message == "" {
		message = "no message"
	}

	if code == "activityLimitReached" || code == "throttled" || code == "serviceNotAvailable" || code == "resourceLocked" {
		time.Sleep(5 * time.Second)
	}
	return fmt.Errorf("graph API error: %s - %s", code, message)
}

func driveItemToFileModel(item *graphDriveItem, ownerEmail, parentID string) *model.File {
	file := &model.File{
		FileID:         item.ID,
		Provider:       "Microsoft",
		OwnerEmail:     ownerEmail,
		FileName:       item.Name,
		FileSize:       item.Size,
		ParentFolderID: parentID,
		CreatedOn:      item.CreatedDateTime,
		LastModified:   item.LastModifiedDateTime,
	}
	if item.Hashes != nil {
		file.FileHash = item.Hashes.QuickXorHash
		file.HashAlgorithm = "quickXorHash"
	} else {
		file.HashAlgorithm = "SHA256"
	}
	return file
}
