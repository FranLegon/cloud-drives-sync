package google

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

type GoogleDrive interface {
	PreFlightCheck(mainEmail string) error
	CreateSyncFolder(mainEmail string) error
	ShareSyncFolder(mainEmail, backupEmail string) error
	ListFilesInSyncFolder(email string) ([]GoogleFile, error)
	ListFolders(email, folderName string) ([]GoogleFolder, error)
	MoveFolderToRoot(email, folderID string) error
	UploadFile(email, path string, content []byte) error
	DownloadFile(email, fileID string) ([]byte, error)
	DeleteFile(email, fileID string) error
	GetQuota(email string) (used, total int64, err error)
	TransferOwnership(fileID, fromEmail, toEmail string) error
	CheckToken(email string) error
}

type GoogleFolder struct {
	ID     string
	Name   string
	IsRoot bool
}

type GoogleFile struct {
	ID       string
	Name     string
	Hash     string
	Size     int64
	MimeType string
	ParentID string
	Created  string
	Modified string
}

// NewGoogleDrive returns a GoogleDrive implementation (stub for now)
func NewGoogleDrive(clientID, clientSecret, refreshToken string) (GoogleDrive, error) {
	ctx := context.Background()
	conf := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Scopes:       []string{"https://www.googleapis.com/auth/drive"},
		Endpoint:     google.Endpoint,
		RedirectURL:  "urn:ietf:wg:oauth:2.0:oob",
	}
	tok := &oauth2.Token{RefreshToken: refreshToken, Expiry: time.Now().Add(-time.Hour)}
	client := conf.Client(ctx, tok)
	return &googleDriveImpl{client: client}, nil
}

type googleDriveImpl struct {
	client *http.Client
	email  string
}

func (g *googleDriveImpl) PreFlightCheck(mainEmail string) error {
	folders, err := g.ListFolders(mainEmail, "synched-cloud-drives")
	if err != nil {
		return fmt.Errorf("[Google][%s] Error listing folders: %v", mainEmail, err)
	}
	if len(folders) != 1 {
		return fmt.Errorf("[Google][%s] Pre-flight failed: found %d 'synched-cloud-drives' folders. Resolve manually.", mainEmail, len(folders))
	}
	if !folders[0].IsRoot {
		if err := g.MoveFolderToRoot(mainEmail, folders[0].ID); err != nil {
			return fmt.Errorf("[Google][%s] Failed to move folder to root: %v", mainEmail, err)
		}
	}
	return nil
}

func (g *googleDriveImpl) CreateSyncFolder(mainEmail string) error {
	body := strings.NewReader(`{"name": "synched-cloud-drives", "mimeType": "application/vnd.google-apps.folder"}`)
	resp, err := g.client.Post("https://www.googleapis.com/drive/v3/files", "application/json", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		return fmt.Errorf("Google API error: %s", resp.Status)
	}
	return nil
}

// ShareSyncFolder shares the sync folder with a backup account (editor access)
func (g *googleDriveImpl) ShareSyncFolder(mainEmail, backupEmail string) error {
	folders, err := g.ListFolders(mainEmail, "synched-cloud-drives")
	if err != nil {
		return err
	}
	if len(folders) != 1 {
		return fmt.Errorf("cannot share: expected 1 sync folder, found %d", len(folders))
	}
	folderID := folders[0].ID
	body := strings.NewReader(fmt.Sprintf(`{"role": "writer", "type": "user", "emailAddress": "%s"}`, backupEmail))
	url := fmt.Sprintf("https://www.googleapis.com/drive/v3/files/%s/permissions?supportsAllDrives=true", folderID)
	req, err := http.NewRequest("POST", url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		return fmt.Errorf("Google API error: %s", resp.Status)
	}
	return nil
}

// ListFilesInSyncFolder lists all files in the sync folder recursively
func (g *googleDriveImpl) ListFilesInSyncFolder(email string) ([]GoogleFile, error) {
	folders, err := g.ListFolders(email, "synched-cloud-drives")
	if err != nil {
		return nil, err
	}
	if len(folders) != 1 {
		return nil, fmt.Errorf("expected 1 sync folder, found %d", len(folders))
	}
	folderID := folders[0].ID
	var files []GoogleFile
	pageToken := ""
	for {
		url := fmt.Sprintf("https://www.googleapis.com/drive/v3/files?q='%s'+in+parents+and+trashed=false&fields=nextPageToken,files(id,name,md5Checksum,size,mimeType,parents,createdTime,modifiedTime)&pageSize=1000", folderID)
		if pageToken != "" {
			url += "&pageToken=" + pageToken
		}
		resp, err := g.client.Get(url)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("Google API error: %s", resp.Status)
		}
		var data struct {
			Files []struct {
				ID       string   `json:"id"`
				Name     string   `json:"name"`
				Hash     string   `json:"md5Checksum"`
				Size     int64    `json:"size,string"`
				MimeType string   `json:"mimeType"`
				ParentID []string `json:"parents"`
				Created  string   `json:"createdTime"`
				Modified string   `json:"modifiedTime"`
			} `json:"files"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
			return nil, err
		}
		for _, f := range data.Files {
			files = append(files, GoogleFile{
				ID:       f.ID,
				Name:     f.Name,
				Hash:     f.Hash,
				Size:     f.Size,
				MimeType: f.MimeType,
				ParentID: firstOrEmpty(f.ParentID),
				Created:  f.Created,
				Modified: f.Modified,
			})
		}
		if data.NextPageToken == "" {
			break
		}
		pageToken = data.NextPageToken
	}
	return files, nil
}

func firstOrEmpty(arr []string) string {
	if len(arr) > 0 {
		return arr[0]
	}
	return ""
}

// UploadFile uploads a file to the sync folder
func (g *googleDriveImpl) UploadFile(email, path string, content []byte) error {
	folders, err := g.ListFolders(email, "synched-cloud-drives")
	if err != nil {
		return err
	}
	if len(folders) != 1 {
		return fmt.Errorf("expected 1 sync folder, found %d", len(folders))
	}
	folderID := folders[0].ID

	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	meta := fmt.Sprintf(`{"name": "%s", "parents": ["%s"]}`, path, folderID)
	metaPart, err := w.CreatePart(map[string][]string{"Content-Type": {"application/json; charset=UTF-8"}})
	if err != nil {
		return err
	}
	if _, err := metaPart.Write([]byte(meta)); err != nil {
		return err
	}
	filePart, err := w.CreatePart(map[string][]string{"Content-Type": {"application/octet-stream"}})
	if err != nil {
		return err
	}
	if _, err := filePart.Write(content); err != nil {
		return err
	}
	w.Close()

	uploadURL := "https://www.googleapis.com/upload/drive/v3/files?uploadType=multipart"
	req, err := http.NewRequest("POST", uploadURL, &b)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		return fmt.Errorf("Google API error: %s", resp.Status)
	}
	return nil
}

// DownloadFile downloads a file by ID
func (g *googleDriveImpl) DownloadFile(email, fileID string) ([]byte, error) {
	url := fmt.Sprintf("https://www.googleapis.com/drive/v3/files/%s?alt=media", fileID)
	resp, err := g.client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Google API error: %s", resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// DeleteFile deletes a file by ID
func (g *googleDriveImpl) DeleteFile(email, fileID string) error {
	url := fmt.Sprintf("https://www.googleapis.com/drive/v3/files/%s", fileID)
	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		return err
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 204 {
		return fmt.Errorf("Google API error: %s", resp.Status)
	}
	return nil
}

// TransferOwnership is not supported in Google Drive API for all file types, but for G Suite files it is
func (g *googleDriveImpl) TransferOwnership(fileID, fromEmail, toEmail string) error {
	// For G Suite files only
	body := strings.NewReader(fmt.Sprintf(`{"role": "owner", "type": "user", "emailAddress": "%s"}`, toEmail))
	url := fmt.Sprintf("https://www.googleapis.com/drive/v3/files/%s/permissions?transferOwnership=true", fileID)
	req, err := http.NewRequest("POST", url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		return fmt.Errorf("Google API error: %s", resp.Status)
	}
	return nil
}

// CheckToken validates the refresh token by making a simple API call
func (g *googleDriveImpl) CheckToken(email string) error {
	resp, err := g.client.Get("https://www.googleapis.com/drive/v3/about?fields=user")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("Google API error: %s", resp.Status)
	}
	return nil
}

func (g *googleDriveImpl) GetQuota(email string) (used, total int64, err error) {
	resp, err := g.client.Get("https://www.googleapis.com/drive/v3/about?fields=storageQuota")
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, 0, fmt.Errorf("Google API error: %s", resp.Status)
	}
	var data struct {
		StorageQuota struct {
			Limit int64 `json:"limit,string"`
			Usage int64 `json:"usage,string"`
		} `json:"storageQuota"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return 0, 0, err
	}
	return data.StorageQuota.Usage, data.StorageQuota.Limit, nil
}

func urlQueryEscape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "'", "%27"), " ", "%20")
}

func (g *googleDriveImpl) ListFolders(email, folderName string) ([]GoogleFolder, error) {
	q := fmt.Sprintf("name='%s' and mimeType='application/vnd.google-apps.folder' and trashed=false", folderName)
	url := "https://www.googleapis.com/drive/v3/files?q=" + urlQueryEscape(q) + "&fields=files(id,name,parents)"
	resp, err := g.client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Google API error: %s", resp.Status)
	}
	var data struct {
		Files []struct {
			ID      string   `json:"id"`
			Name    string   `json:"name"`
			Parents []string `json:"parents"`
		} `json:"files"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	folders := []GoogleFolder{}
	for _, f := range data.Files {
		isRoot := len(f.Parents) == 0 || (len(f.Parents) == 1 && f.Parents[0] == "root")
		folders = append(folders, GoogleFolder{ID: f.ID, Name: f.Name, IsRoot: isRoot})
	}
	return folders, nil
}

func (g *googleDriveImpl) MoveFolderToRoot(email, folderID string) error {
	// Get folder metadata to find current parents
	getURL := fmt.Sprintf("https://www.googleapis.com/drive/v3/files/%s?fields=parents", folderID)
	resp, err := g.client.Get(getURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("Google API error: %s", resp.Status)
	}
	var meta struct {
		Parents []string `json:"parents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return err
	}
	if len(meta.Parents) == 0 {
		return nil // Already at root
	}
	// Remove from all parents, add to root
	patchURL := fmt.Sprintf("https://www.googleapis.com/drive/v3/files/%s?addParents=root&removeParents=%s", folderID, strings.Join(meta.Parents, ","))
	req, err := http.NewRequest("PATCH", patchURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp2, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != 200 {
		return fmt.Errorf("Google API error: %s", resp2.Status)
	}
	return nil
}
