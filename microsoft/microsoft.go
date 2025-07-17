package microsoft

import (
	"cloud-drives-sync/config"
	"cloud-drives-sync/database"
	"cloud-drives-sync/retry"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Required constants for OAuth2 flow
const (
	authEndpoint  = "https://login.microsoftonline.com/common/oauth2/v2.0/authorize"
	tokenEndpoint = "https://login.microsoftonline.com/common/oauth2/v2.0/token"
	redirectURI   = "http://localhost:8080/oauth2callback"
	scope         = "offline_access Files.ReadWrite.All User.Read email openid profile"
	graphAPIBase  = "https://graph.microsoft.com/v1.0"
)

// Token response struct for OAuth2
type tokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

// Stub for PKCE code verifier generator
func genCodeVerifier() (string, string) {
	b := make([]byte, 32)
	rand.Read(b)
	verifier := base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(h[:])
	return verifier, challenge
}

func openBrowser(url string) {
	// Windows
	exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

// --- Authorization Code Grant with PKCE ---
func getTokenAuthCode(clientID, clientSecret string) (string, string, error) {
	verifier, challenge := genCodeVerifier()
	state := fmt.Sprintf("st%d", time.Now().UnixNano())
	authURL := fmt.Sprintf("%s?client_id=%s&response_type=code&redirect_uri=%s&response_mode=query&scope=%s&state=%s&code_challenge=%s&code_challenge_method=S256", authEndpoint, url.QueryEscape(clientID), url.QueryEscape(redirectURI), url.QueryEscape(scope), url.QueryEscape(state), url.QueryEscape(challenge))

	codeCh := make(chan string)
	srv := &http.Server{Addr: ":8080"}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth2callback", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state {
			http.Error(w, "State mismatch", http.StatusBadRequest)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "No code", http.StatusBadRequest)
			return
		}
		fmt.Fprintf(w, "Authentication complete. You may close this window.")
		codeCh <- code
	})
	srv.Handler = mux

	go func() {
		srv.ListenAndServe()
	}()

	go func() {
		openBrowser(authURL)
	}()

	var code string

	select {
	case code = <-codeCh:
		srv.Shutdown(context.TODO())
	case <-time.After(180 * time.Second):
		srv.Shutdown(context.TODO())
		return "", "", errors.New("timeout waiting for auth code")
	}

	// Exchange code for tokens
	data := url.Values{}
	data.Set("client_id", clientID)
	data.Set("scope", scope)
	data.Set("code", code)
	data.Set("redirect_uri", redirectURI)
	data.Set("grant_type", "authorization_code")
	data.Set("code_verifier", verifier)
	if clientSecret != "" {
		data.Set("client_secret", clientSecret)
	}
	var resp *http.Response
	err := retry.Retry(5, time.Second, func() error {
		var apiErr error
		resp, apiErr = http.PostForm(tokenEndpoint, data)
		return apiErr
	})
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	var tr tokenResp
	body, _ := io.ReadAll(resp.Body)
	json.Unmarshal(body, &tr)
	if tr.AccessToken != "" && tr.RefreshToken != "" {
		return tr.AccessToken, tr.RefreshToken, nil
	}
	return "", "", errors.New("Failed to get token: " + string(body))
}

func getAccessToken(clientID, clientSecret, refreshToken string) (string, error) {
	data := url.Values{}
	data.Set("client_id", clientID)
	data.Set("client_secret", clientSecret)
	data.Set("refresh_token", refreshToken)
	data.Set("grant_type", "refresh_token")
	data.Set("scope", scope)
	var resp *http.Response
	err := retry.Retry(5, time.Second, func() error {
		var apiErr error
		resp, apiErr = http.PostForm(tokenEndpoint, data)
		return apiErr
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var tr tokenResp
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", err
	}
	if tr.AccessToken == "" {
		return "", errors.New("no access token returned")
	}
	return tr.AccessToken, nil
}

func getAuthClient(creds config.ClientCreds, refreshToken string) (*http.Client, string, error) {
	accessToken, err := getAccessToken(creds.ID, creds.Secret, refreshToken)
	if err != nil {
		return nil, "", err
	}
	client := &http.Client{}
	return client, accessToken, nil
}

func getUserEmail(client *http.Client, accessToken string) (string, error) {
	req, _ := http.NewRequest("GET", graphAPIBase+"/me", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var data struct{ Mail, UserPrincipalName string }
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}
	if data.Mail != "" {
		return data.Mail, nil
	}
	return data.UserPrincipalName, nil
}

func AddMainAccount(cfg *config.Config, pw, configPath string) {
	fmt.Println("Starting Microsoft main account OAuth2 authorization code flow...")
	accessToken, refreshToken, err := getTokenAuthCode(cfg.MicrosoftClient.ID, cfg.MicrosoftClient.Secret)
	if err != nil {
		fmt.Println("Error during authorization code flow:", err)
		return
	}
	client := &http.Client{}
	email, err := getUserEmail(client, accessToken)
	if err != nil {
		fmt.Println("Failed to get user email:", err)
		return
	}
	user := config.User{
		Provider:     "Microsoft",
		Email:        email,
		IsMain:       true,
		RefreshToken: refreshToken,
	}
	cfg.Users = append(cfg.Users, user)
	if err := config.EncryptAndSaveConfig(*cfg, configPath, pw); err != nil {
		fmt.Println("Failed to save config:", err)
		return
	}
	fmt.Println("Microsoft main account added.")
}

func AddBackupAccount(cfg *config.Config, pw, configPath string) {
	fmt.Println("Starting Microsoft backup account OAuth2 authorization code flow...")
	accessToken, refreshToken, err := getTokenAuthCode(cfg.MicrosoftClient.ID, cfg.MicrosoftClient.Secret)
	if err != nil {
		fmt.Println("Error during authorization code flow:", err)
		return
	}
	client := &http.Client{}
	email, err := getUserEmail(client, accessToken)
	if err != nil {
		fmt.Println("Failed to get user email:", err)
		return
	}
	user := config.User{
		Provider:     "Microsoft",
		Email:        email,
		IsMain:       false,
		RefreshToken: refreshToken,
	}
	cfg.Users = append(cfg.Users, user)
	if err := config.EncryptAndSaveConfig(*cfg, configPath, pw); err != nil {
		fmt.Println("Failed to save config:", err)
		return
	}
	fmt.Println("Microsoft backup account added.")
}

func EnsureSyncFolder(u config.User, creds config.ClientCreds, pw string) {
	client, accessToken, err := getAuthClient(creds, u.RefreshToken)
	if err != nil {
		fmt.Println("Auth error:", err)
		return
	}
	// Check if folder exists
	req, _ := http.NewRequest("GET", graphAPIBase+"/me/drive/root/children", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	var resp *http.Response
	err = retry.Retry(5, time.Second, func() error {
		var apiErr error
		resp, apiErr = client.Do(req)
		return apiErr
	})
	if err != nil {
		fmt.Println("Failed to list root children:", err)
		return
	}
	defer resp.Body.Close()
	var data struct {
		Value []struct {
			Name, Id string
			Folder   *struct{}
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		fmt.Println("Failed to decode children:", err)
		return
	}
	count := 0
	for _, item := range data.Value {
		if item.Name == "synched-cloud-drives" && item.Folder != nil {
			count++
		}
	}
	if count == 0 {
		// Create folder
		body := strings.NewReader(`{"name": "synched-cloud-drives", "folder": {}, "@microsoft.graph.conflictBehavior": "rename"}`)
		req, _ := http.NewRequest("POST", graphAPIBase+"/me/drive/root/children", body)
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Content-Type", "application/json")
		var resp2 *http.Response
		err = retry.Retry(5, time.Second, func() error {
			var apiErr error
			resp2, apiErr = client.Do(req)
			return apiErr
		})
		if err != nil {
			fmt.Println("Failed to create folder:", err)
			return
		}
		defer resp2.Body.Close()
		if resp2.StatusCode != 201 {
			fmt.Println("Failed to create synched-cloud-drives folder.")
			return
		}
		fmt.Println("Created synched-cloud-drives folder.")
	} else if count > 1 {
		fmt.Println("Multiple synched-cloud-drives folders found. Please resolve manually.")
	}
}

func PreFlightCheck(u config.User, creds config.ClientCreds, pw string) error {
	client, accessToken, err := getAuthClient(creds, u.RefreshToken)
	if err != nil {
		return fmt.Errorf("[Microsoft][%s] API error: %v", u.Email, err)
	}
	req, _ := http.NewRequest("GET", graphAPIBase+"/me/drive/root/children", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	var resp *http.Response
	err = retry.Retry(5, time.Second, func() error {
		var apiErr error
		resp, apiErr = client.Do(req)
		return apiErr
	})
	if err != nil {
		return fmt.Errorf("[Microsoft][%s] API error: %v", u.Email, err)
	}
	defer resp.Body.Close()
	var data struct {
		Value []struct {
			Name      string
			Id        string
			Folder    *struct{}
			CreatedBy struct {
				User struct {
					Email string `json:"email"`
				} `json:"user"`
			} `json:"createdBy"`
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return fmt.Errorf("[Microsoft][%s] decode error: %v", u.Email, err)
	}
	var folders []struct {
		Name      string
		Id        string
		CreatedBy string
	}
	for _, item := range data.Value {
		if item.Name == "synched-cloud-drives" && item.Folder != nil {
			createdBy := ""
			if item.CreatedBy.User.Email != "" {
				createdBy = item.CreatedBy.User.Email
			}
			folders = append(folders, struct {
				Name      string
				Id        string
				CreatedBy string
			}{item.Name, item.Id, createdBy})
		}
	}
	if len(folders) == 0 {
		return fmt.Errorf("[Microsoft][%s] No accessible synched-cloud-drives folder found in root", u.Email)
	}
	if len(folders) > 1 {
		return fmt.Errorf("[Microsoft][%s] Multiple synched-cloud-drives folders found in root. Please resolve manually", u.Email)
	}
	folder := folders[0]
	// Check if folder is owned by main account
	if folder.CreatedBy != u.Email {
		return fmt.Errorf("[Microsoft][%s] synched-cloud-drives folder is not owned by main account. Please transfer ownership", u.Email)
	}
	fmt.Printf("[Microsoft][%s] Pre-flight check passed: unique, in root, and owned by main account.\n", u.Email)
	return nil
}

func ScanAndUpdateMetadata(u config.User, creds config.ClientCreds, pw string, db database.DatabaseInterface) {
	client, accessToken, err := getAuthClient(creds, u.RefreshToken)
	if err != nil {
		fmt.Println("Auth error:", err)
		return
	}
	// Find synched-cloud-drives folder
	req, _ := http.NewRequest("GET", graphAPIBase+"/me/drive/root/children", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	var resp *http.Response
	err = retry.Retry(5, time.Second, func() error {
		var apiErr error
		resp, apiErr = client.Do(req)
		return apiErr
	})
	if err != nil {
		fmt.Println("Failed to list root children:", err)
		return
	}
	defer resp.Body.Close()
	var data struct {
		Value []struct {
			Name, Id string
			Folder   *struct{}
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		fmt.Println("Failed to decode children:", err)
		return
	}
	var rootID string
	for _, item := range data.Value {
		if item.Name == "synched-cloud-drives" && item.Folder != nil {
			rootID = item.Id
		}
	}
	if rootID == "" {
		fmt.Println("synched-cloud-drives folder not found.")
		return
	}
	scanFolder(client, accessToken, u, rootID, "synched-cloud-drives", db)
}

func scanFolder(client *http.Client, accessToken string, u config.User, folderID, folderName string, db database.DatabaseInterface) {
	req, _ := http.NewRequest("GET", graphAPIBase+"/me/drive/items/"+folderID+"/children", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	var resp *http.Response
	err := retry.Retry(5, time.Second, func() error {
		var apiErr error
		resp, apiErr = client.Do(req)
		return apiErr
	})
	if err != nil {
		fmt.Println("Failed to list children:", err)
		return
	}
	defer resp.Body.Close()
	var data struct {
		Value []struct {
			Name, Id                              string
			Folder                                *struct{}
			File                                  *struct{}
			Size                                  *int64
			CreatedDateTime, LastModifiedDateTime string
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		fmt.Println("Failed to decode children:", err)
		return
	}
	for _, item := range data.Value {
		if item.Folder != nil {
			scanFolder(client, accessToken, u, item.Id, folderName+"/"+item.Name, db)
		} else if item.File != nil {
			// Download file content to hash
			req, _ := http.NewRequest("GET", graphAPIBase+"/me/drive/items/"+item.Id+"/content", nil)
			req.Header.Set("Authorization", "Bearer "+accessToken)
			var resp2 *http.Response
			err := retry.Retry(5, time.Second, func() error {
				var apiErr error
				resp2, apiErr = client.Do(req)
				return apiErr
			})
			if err != nil {
				fmt.Println("Failed to download file:", err)
				continue
			}
			hash := database.HashReader(resp2.Body)
			resp2.Body.Close()
			db.UpsertFile(database.FileRecord{
				FileID:           item.Id,
				Provider:         "Microsoft",
				OwnerEmail:       u.Email,
				FileHash:         hash,
				FileName:         item.Name,
				FileSize:         derefInt64(item.Size),
				FileExtension:    getFileExt(item.Name),
				ParentFolderID:   folderID,
				ParentFolderName: folderName,
				CreatedOn:        item.CreatedDateTime,
				LastModified:     item.LastModifiedDateTime,
				LastSynced:       time.Now().Format(time.RFC3339),
			})
		}
	}
}

func derefInt64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func getFileExt(name string) string {
	i := strings.LastIndex(name, ".")
	if i == -1 {
		return ""
	}
	return name[i:]
}

func DeleteFile(f database.FileRecord) {
	// Delete file by ID
	client, accessToken, err := getAuthClient(config.ClientCreds{ID: os.Getenv("MS_CLIENT_ID"), Secret: os.Getenv("MS_CLIENT_SECRET")}, f.OwnerEmail)
	if err != nil {
		fmt.Println("Auth error:", err)
		return
	}
	req, _ := http.NewRequest("DELETE", graphAPIBase+"/me/drive/items/"+f.FileID, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	var resp *http.Response
	err = retry.Retry(5, time.Second, func() error {
		var apiErr error
		resp, apiErr = client.Do(req)
		return apiErr
	})
	if err != nil {
		fmt.Println("Failed to delete file:", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == 204 {
		fmt.Println("File deleted from Microsoft account.")
	} else {
		fmt.Println("Failed to delete file from Microsoft account.")
	}
}

func ShareSyncFolderWith(main, backup *config.User, creds config.ClientCreds, pw string) {
	// Grant editor access to backup user for the main's synched-cloud-drives folder
	client, accessToken, err := getAuthClient(creds, main.RefreshToken)
	if err != nil {
		fmt.Println("Auth error:", err)
		return
	}
	// Find folder ID
	req, _ := http.NewRequest("GET", graphAPIBase+"/me/drive/root/children", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	var resp *http.Response
	err = retry.Retry(5, time.Second, func() error {
		var apiErr error
		resp, apiErr = client.Do(req)
		return apiErr
	})
	if err != nil {
		fmt.Println("Failed to list root children:", err)
		return
	}
	defer resp.Body.Close()
	var data struct {
		Value []struct {
			Name, Id string
			Folder   *struct{}
		}
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		fmt.Println("Failed to decode children:", err)
		return
	}
	var folderID string
	for _, item := range data.Value {
		if item.Name == "synched-cloud-drives" && item.Folder != nil {
			folderID = item.Id
		}
	}
	if folderID == "" {
		fmt.Println("synched-cloud-drives folder not found.")
		return
	}
	// Share folder
	body := strings.NewReader(fmt.Sprintf(`{"requireSignIn":true,"sendInvitation":true,"roles":["write"],"recipients":[{"email":"%s"}]}`, backup.Email))
	req, _ = http.NewRequest("POST", graphAPIBase+"/me/drive/items/"+folderID+"/invite", body)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	var resp2 *http.Response
	err = retry.Retry(5, time.Second, func() error {
		var apiErr error
		resp2, apiErr = client.Do(req)
		return apiErr
	})
	if err != nil {
		fmt.Println("Failed to share folder:", err)
		return
	}
	defer resp2.Body.Close()
	if resp2.StatusCode == 200 {
		fmt.Println("Folder shared with backup account.")
	} else {
		fmt.Println("Failed to share folder with backup account.")
	}
}

func UploadFileFromGoogle(f database.FileRecord, cfg config.Config, pw string) {
	// Download file from Google, upload to Microsoft
	fmt.Println("Uploading file from Google to Microsoft is not yet implemented in this stub.")
	// You would use Google API to download, then POST to /me/drive/items/{parent-id}/children/content
}

func GetQuota(u config.User, creds config.ClientCreds, pw string) float64 {
	client, accessToken, err := getAuthClient(creds, u.RefreshToken)
	if err != nil {
		fmt.Println("Auth error:", err)
		return 0
	}
	req, _ := http.NewRequest("GET", graphAPIBase+"/me/drive", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	var resp *http.Response
	err = retry.Retry(5, time.Second, func() error {
		var apiErr error
		resp, apiErr = client.Do(req)
		return apiErr
	})
	if err != nil {
		fmt.Println("Failed to get drive info:", err)
		return 0
	}
	defer resp.Body.Close()
	var data struct{ Quota struct{ Used, Total float64 } }
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		fmt.Println("Failed to decode drive info:", err)
		return 0
	}
	if data.Quota.Total == 0 {
		return 0
	}
	return data.Quota.Used / data.Quota.Total
}

func TransferFileOwnership(f database.FileRecord, from, to string, creds config.ClientCreds, pw string) {
	// Microsoft Graph does not support direct transfer of file ownership; you can share and remove the old owner
	fmt.Println("Direct file ownership transfer is not supported by Microsoft Graph API. Use ShareSyncFolderWith instead.")
}

func CheckToken(u config.User, creds config.ClientCreds, pw string) bool {
	client, accessToken, err := getAuthClient(creds, u.RefreshToken)
	if err != nil {
		return false
	}
	req, _ := http.NewRequest("GET", graphAPIBase+"/me", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	var resp *http.Response
	err = retry.Retry(5, time.Second, func() error {
		var apiErr error
		resp, apiErr = client.Do(req)
		return apiErr
	})
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

// Download file from OneDrive (stub)
func DownloadFile(f database.FileRecord, u config.User, creds config.ClientCreds, pw string) io.ReadCloser {
	// ...actual implementation needed...
	return nil
}

// Upload file to OneDrive (stub)
func UploadFile(r io.Reader, f database.FileRecord, u config.User, creds config.ClientCreds, pw string) string {
	// ...actual implementation needed...
	return "new-file-id"
}
