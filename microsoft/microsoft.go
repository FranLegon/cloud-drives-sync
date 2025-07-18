package microsoft

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

type OneDrive interface {
	PreFlightCheck(mainEmail string) error
	CreateSyncFolder(mainEmail string) error
	ShareSyncFolder(mainEmail, backupEmail string) error
	ListFilesInSyncFolder(email string) ([]OneDriveFile, error)
	ListFolders(email, folderName string) ([]OneDriveFolder, error)
	MoveFolderToRoot(email, folderID string) error
	UploadFile(email, path string, content []byte) error
	DownloadFile(email, fileID string) ([]byte, error)
	DeleteFile(email, fileID string) error
	GetQuota(email string) (used, total int64, err error)
	TransferOwnership(fileID, fromEmail, toEmail string) error
	CheckToken(email string) error
}

type OneDriveFolder struct {
	ID     string
	Name   string
	IsRoot bool
}

type OneDriveFile struct {
	ID       string
	Name     string
	Hash     string
	Size     int64
	MimeType string
	ParentID string
	Created  string
	Modified string
}

type oneDriveImpl struct {
	client *http.Client
	email  string
}

// NewOneDrive returns a OneDrive implementation (stub for now)
func NewOneDrive(clientID, clientSecret, refreshToken string) (OneDrive, error) {
	ctx := context.Background()
	conf := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes:       []string{"Files.ReadWrite.All", "offline_access"},
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
			TokenURL: "https://login.microsoftonline.com/common/oauth2/v2.0/token",
		},
	}
	tok := &oauth2.Token{RefreshToken: refreshToken, Expiry: time.Now().Add(-time.Hour)}
	client := conf.Client(ctx, tok)
	return &oneDriveImpl{client: client}, nil
}

func (o *oneDriveImpl) PreFlightCheck(mainEmail string) error {
	folders, err := o.ListFolders(mainEmail, "synched-cloud-drives")
	if err != nil {
		return fmt.Errorf("[Microsoft][%s] Error listing folders: %v", mainEmail, err)
	}
	if len(folders) != 1 {
		return fmt.Errorf("[Microsoft][%s] Pre-flight failed: found %d 'synched-cloud-drives' folders. Resolve manually.", mainEmail, len(folders))
	}
	if !folders[0].IsRoot {
		if err := o.MoveFolderToRoot(mainEmail, folders[0].ID); err != nil {
			return fmt.Errorf("[Microsoft][%s] Failed to move folder to root: %v", mainEmail, err)
		}
	}
	return nil
}

func (o *oneDriveImpl) CreateSyncFolder(mainEmail string) error {
	body := strings.NewReader(`{"name": "synched-cloud-drives", "folder": {}, "@microsoft.graph.conflictBehavior": "fail"}`)
	resp, err := o.client.Post("https://graph.microsoft.com/v1.0/me/drive/root/children", "application/json", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		return fmt.Errorf("Microsoft Graph API error: %s", resp.Status)
	}
	return nil
}

func (o *oneDriveImpl) ShareSyncFolder(mainEmail, backupEmail string) error {
	folders, err := o.ListFolders(mainEmail, "synched-cloud-drives")
	if err != nil {
		return err
	}
	if len(folders) != 1 {
		return fmt.Errorf("cannot share: expected 1 sync folder, found %d", len(folders))
	}
	folderID := folders[0].ID
	body := bytes.NewBufferString(fmt.Sprintf(`{"recipients": [{"email": "%s"}], "requireSignIn": true, "sendInvitation": true, "roles": ["write"]}`, backupEmail))
	url := fmt.Sprintf("https://graph.microsoft.com/v1.0/me/drive/items/%s/invite", folderID)
	req, err := http.NewRequest("POST", url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		return fmt.Errorf("Microsoft Graph API error: %s", resp.Status)
	}
	return nil
}

func (o *oneDriveImpl) ListFilesInSyncFolder(email string) ([]OneDriveFile, error) {
	folders, err := o.ListFolders(email, "synched-cloud-drives")
	if err != nil {
		return nil, err
	}
	if len(folders) != 1 {
		return nil, fmt.Errorf("expected 1 sync folder, found %d", len(folders))
	}
	folderID := folders[0].ID
	var files []OneDriveFile
	url := fmt.Sprintf("https://graph.microsoft.com/v1.0/me/drive/items/%s/children", folderID)
	resp, err := o.client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Microsoft Graph API error: %s", resp.Status)
	}
	var data struct {
		Value []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Size     int64  `json:"size"`
			MimeType string `json:"file.mimeType"`
			Created  string `json:"createdDateTime"`
			Modified string `json:"lastModifiedDateTime"`
		} `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	for _, f := range data.Value {
		files = append(files, OneDriveFile{
			ID:       f.ID,
			Name:     f.Name,
			Size:     f.Size,
			MimeType: f.MimeType,
			Created:  f.Created,
			Modified: f.Modified,
		})
	}
	return files, nil
}

func (o *oneDriveImpl) ListFolders(email, folderName string) ([]OneDriveFolder, error) {
	url := "https://graph.microsoft.com/v1.0/me/drive/root/children?$filter=folder ne null"
	resp, err := o.client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Microsoft Graph API error: %s", resp.Status)
	}
	var data struct {
		Value []struct {
			ID              string `json:"id"`
			Name            string `json:"name"`
			ParentReference struct {
				ID string `json:"id"`
			} `json:"parentReference"`
		} `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	folders := []OneDriveFolder{}
	for _, f := range data.Value {
		isRoot := f.ParentReference.ID == "root"
		if f.Name == folderName {
			folders = append(folders, OneDriveFolder{ID: f.ID, Name: f.Name, IsRoot: isRoot})
		}
	}
	return folders, nil
}

func (o *oneDriveImpl) MoveFolderToRoot(email, folderID string) error {
	patchURL := fmt.Sprintf("https://graph.microsoft.com/v1.0/me/drive/items/%s", folderID)
	body := strings.NewReader(`{"parentReference": {"id": "root"}}`)
	req, err := http.NewRequest("PATCH", patchURL, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("Microsoft Graph API error: %s", resp.Status)
	}
	return nil
}

func (o *oneDriveImpl) UploadFile(email, path string, content []byte) error {
	folders, err := o.ListFolders(email, "synched-cloud-drives")
	if err != nil {
		return err
	}
	if len(folders) != 1 {
		return fmt.Errorf("expected 1 sync folder, found %d", len(folders))
	}
	folderID := folders[0].ID
	url := fmt.Sprintf("https://graph.microsoft.com/v1.0/me/drive/items/%s:/%s:/content", folderID, path)
	req, err := http.NewRequest("PUT", url, bytes.NewReader(content))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := o.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		return fmt.Errorf("Microsoft Graph API error: %s", resp.Status)
	}
	return nil
}

func (o *oneDriveImpl) DownloadFile(email, fileID string) ([]byte, error) {
	url := fmt.Sprintf("https://graph.microsoft.com/v1.0/me/drive/items/%s/content", fileID)
	resp, err := o.client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Microsoft Graph API error: %s", resp.Status)
	}
	return io.ReadAll(resp.Body)
}

func (o *oneDriveImpl) DeleteFile(email, fileID string) error {
	url := fmt.Sprintf("https://graph.microsoft.com/v1.0/me/drive/items/%s", fileID)
	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		return err
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 204 {
		return fmt.Errorf("Microsoft Graph API error: %s", resp.Status)
	}
	return nil
}

func (o *oneDriveImpl) TransferOwnership(fileID, fromEmail, toEmail string) error {
	// OneDrive does not support direct ownership transfer; must copy to new owner
	return fmt.Errorf("OneDrive: direct ownership transfer not supported; use download/upload workaround")
}

func (o *oneDriveImpl) CheckToken(email string) error {
	resp, err := o.client.Get("https://graph.microsoft.com/v1.0/me")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("Microsoft Graph API error: %s", resp.Status)
	}
	return nil
}

func (o *oneDriveImpl) GetQuota(email string) (used, total int64, err error) {
	resp, err := o.client.Get("https://graph.microsoft.com/v1.0/me/drive")
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, 0, fmt.Errorf("Microsoft Graph API error: %s", resp.Status)
	}
	var data struct {
		Quota struct {
			Total int64 `json:"total"`
			Used  int64 `json:"used"`
		} `json:"quota"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return 0, 0, err
	}
	return data.Quota.Used, data.Quota.Total, nil
}
