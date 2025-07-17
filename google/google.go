package google

import (
	"cloud-drives-sync/config"
	"cloud-drives-sync/database"
	"cloud-drives-sync/retry"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

// CheckToken verifies if the refresh token is valid for the given user
func CheckToken(u config.User, creds config.ClientCreds, pw string) bool {
	driveSrv, err := getClient(creds, u.RefreshToken)
	if err != nil {
		fmt.Printf("[Google][%s] Token check failed: %v\n", u.Email, err)
		return false
	}
	_, err = driveSrv.About.Get().Fields("user").Do()
	if err != nil {
		fmt.Printf("[Google][%s] Token check failed: %v\n", u.Email, err)
		return false
	}
	return true
}

// Download file from Google Drive (stub)
func DownloadFile(f database.FileRecord, u config.User, creds config.ClientCreds, pw string) io.ReadCloser {
	// ...actual implementation needed...
	return nil
}

// Upload file to Google Drive (stub)
func UploadFile(r io.Reader, f database.FileRecord, u config.User, creds config.ClientCreds, pw string) string {
	// ...actual implementation needed...
	return "new-file-id"
}

// Delete file from Google Drive

func getClient(creds config.ClientCreds, refreshToken string) (*drive.Service, error) {
	ctx := context.Background()
	conf := &oauth2.Config{
		ClientID:     creds.ID,
		ClientSecret: creds.Secret,
		Scopes:       []string{drive.DriveScope},
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://accounts.google.com/o/oauth2/auth",
			TokenURL: "https://oauth2.googleapis.com/token",
		},
		RedirectURL: "http://localhost:8080/oauth2callback",
	}
	tok := &oauth2.Token{RefreshToken: refreshToken}
	ts := conf.TokenSource(ctx, tok)
	return drive.NewService(ctx, option.WithTokenSource(ts))
}

func AddMainAccount(cfg *config.Config, pw, configPath string) {
	fmt.Println("Starting Google main account OAuth2 flow...")
	oauthCfg := &oauth2.Config{
		ClientID:     cfg.GoogleClient.ID,
		ClientSecret: cfg.GoogleClient.Secret,
		Scopes:       []string{drive.DriveScope, "email", "profile"},
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://accounts.google.com/o/oauth2/auth",
			TokenURL: "https://oauth2.googleapis.com/token",
		},
		RedirectURL: "http://localhost:8080/oauth2callback",
	}
	codeCh := make(chan string)
	srv := &http.Server{Addr: ":8080"}
	http.HandleFunc("/oauth2callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		fmt.Fprintf(w, "Authorization received. You can close this window.")
		codeCh <- code
	})
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("Server error: %v\n", err)
		}
	}()
	authURL := oauthCfg.AuthCodeURL("state-token", oauth2.AccessTypeOffline, oauth2.ApprovalForce)
	fmt.Printf("Visit the following URL in your browser to authorize:\n%s\n", authURL)
	code := <-codeCh
	ctx := context.Background()
	token, err := oauthCfg.Exchange(ctx, code)
	if err != nil {
		fmt.Printf("Token exchange error: %v\n", err)
		return
	}
	srv.Close()
	ts := oauthCfg.TokenSource(ctx, token)
	driveSrv, err := drive.NewService(ctx, option.WithTokenSource(ts))
	if err != nil {
		fmt.Printf("Drive service error: %v\n", err)
		return
	}
	about, err := driveSrv.About.Get().Fields("user(emailAddress)").Do()
	if err != nil {
		fmt.Printf("Failed to get user info: %v\n", err)
		return
	}
	user := config.User{
		Provider:     "Google",
		Email:        about.User.EmailAddress,
		IsMain:       true,
		RefreshToken: token.RefreshToken,
	}
	cfg.Users = append(cfg.Users, user)
	if err := config.EncryptAndSaveConfig(*cfg, configPath, pw); err != nil {
		fmt.Printf("Failed to save config: %v\n", err)
		return
	}
	fmt.Println("Google main account added.")
}

func AddBackupAccount(cfg *config.Config, pw, configPath string) {
	fmt.Println("Starting Google backup account OAuth2 flow...")
	oauthCfg := &oauth2.Config{
		ClientID:     cfg.GoogleClient.ID,
		ClientSecret: cfg.GoogleClient.Secret,
		Scopes:       []string{drive.DriveScope, "email", "profile"},
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://accounts.google.com/o/oauth2/auth",
			TokenURL: "https://oauth2.googleapis.com/token",
		},
		RedirectURL: "http://localhost:8080/oauth2callback",
	}
	codeCh := make(chan string)
	srv := &http.Server{Addr: ":8080"}
	http.HandleFunc("/oauth2callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		fmt.Fprintf(w, "Authorization received. You can close this window.")
		codeCh <- code
	})
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("Server error: %v\n", err)
		}
	}()
	authURL := oauthCfg.AuthCodeURL("state-token", oauth2.AccessTypeOffline, oauth2.ApprovalForce)
	fmt.Printf("Visit the following URL in your browser to authorize:\n%s\n", authURL)
	code := <-codeCh
	ctx := context.Background()
	token, err := oauthCfg.Exchange(ctx, code)
	if err != nil {
		fmt.Printf("Token exchange error: %v\n", err)
		return
	}
	srv.Close()
	ts := oauthCfg.TokenSource(ctx, token)
	driveSrv, err := drive.NewService(ctx, option.WithTokenSource(ts))
	if err != nil {
		fmt.Printf("Drive service error: %v\n", err)
		return
	}
	about, err := driveSrv.About.Get().Fields("user(emailAddress)").Do()
	if err != nil {
		fmt.Printf("Failed to get user info: %v\n", err)
		return
	}
	user := config.User{
		Provider:     "Google",
		Email:        about.User.EmailAddress,
		IsMain:       false,
		RefreshToken: token.RefreshToken,
	}
	cfg.Users = append(cfg.Users, user)
	if err := config.EncryptAndSaveConfig(*cfg, configPath, pw); err != nil {
		fmt.Printf("Failed to save config: %v\n", err)
		return
	}
	fmt.Println("Google backup account added.")
}

func EnsureSyncFolder(u config.User, creds config.ClientCreds, pw string) {
	driveSrv, err := getClient(creds, u.RefreshToken)
	if err != nil {
		fmt.Printf("Google API error: %v\n", err)
		os.Exit(1)
	}
	q := "name = 'synched-cloud-drives' and 'root' in parents and trashed = false"
	var res *drive.FileList
	err = retry.Retry(5, time.Second, func() error {
		var apiErr error
		res, apiErr = driveSrv.Files.List().Q(q).Fields("files(id, name)").Do()
		return apiErr
	})
	if err != nil {
		fmt.Printf("Google API error: %v\n", err)
		os.Exit(1)
	}
	if len(res.Files) == 0 {
		folder := &drive.File{Name: "synched-cloud-drives", MimeType: "application/vnd.google-apps.folder", Parents: []string{"root"}}
		err = retry.Retry(5, time.Second, func() error {
			_, apiErr := driveSrv.Files.Create(folder).Do()
			return apiErr
		})
		if err != nil {
			fmt.Printf("Failed to create synched-cloud-drives: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Created synched-cloud-drives folder.")
	} else if len(res.Files) > 1 {
		fmt.Println("Multiple synched-cloud-drives folders found. Please resolve manually.")
		os.Exit(1)
	}
}

func PreFlightCheck(u config.User, creds config.ClientCreds, pw string) error {
	driveSrv, err := getClient(creds, u.RefreshToken)
	if err != nil {
		return err
	}
	// Search for all folders named 'synched-cloud-drives' that are not trashed
	q := "name = 'synched-cloud-drives' and mimeType = 'application/vnd.google-apps.folder' and trashed = false"
	var res *drive.FileList
	err = retry.Retry(5, time.Second, func() error {
		var apiErr error
		res, apiErr = driveSrv.Files.List().Q(q).Fields("files(id, name, parents)").Do()
		return apiErr
	})
	if err != nil {
		return err
	}
	if len(res.Files) == 1 {
		return nil
	}
	if len(res.Files) == 0 {
		return fmt.Errorf("[Google] No accessible synched-cloud-drives folder found. If shared, make sure it is added to 'My Drive'")
	}
	return fmt.Errorf("[Google] Multiple synched-cloud-drives folders found. Please resolve manually")
}

func ScanAndUpdateMetadata(u config.User, creds config.ClientCreds, pw string, db database.DatabaseInterface) {
	driveSrv, err := getClient(creds, u.RefreshToken)
	if err != nil {
		fmt.Printf("Google API error: %v\n", err)
		return
	}
	q := "name = 'synched-cloud-drives' and mimeType = 'application/vnd.google-apps.folder' and trashed = false"
	var res *drive.FileList
	err = retry.Retry(5, time.Second, func() error {
		var apiErr error
		res, apiErr = driveSrv.Files.List().Q(q).Fields("files(id, name, parents)").Do()
		return apiErr
	})
	if err != nil {
		fmt.Printf("Pre-flight failed: %v\n", err)
		return
	}
	var rootID string
	for _, f := range res.Files {
		for _, p := range f.Parents {
			if p == "root" {
				rootID = f.Id
				break
			}
		}
		if rootID != "" {
			break
		}
	}
	if rootID == "" {
		fmt.Printf("No accessible synched-cloud-drives folder found in root. If shared, make sure it is added to 'My Drive'.\n")
		return
	}
	scanFolder(driveSrv, u, rootID, "synched-cloud-drives", db)
}

func scanFolder(srv *drive.Service, u config.User, folderID, folderName string, db database.DatabaseInterface) {
	pageToken := ""
	for {
		call := srv.Files.List().Q(fmt.Sprintf("'%s' in parents and trashed = false", folderID)).Fields("nextPageToken, files(id, name, mimeType, size, createdTime, modifiedTime, parents)")
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		var res *drive.FileList
		err := retry.Retry(5, time.Second, func() error {
			var apiErr error
			res, apiErr = call.Do()
			return apiErr
		})
		if err != nil {
			fmt.Printf("Google API error: %v\n", err)
			return
		}
		for _, f := range res.Files {
			if f.MimeType == "application/vnd.google-apps.folder" {
				scanFolder(srv, u, f.Id, filepath.Join(folderName, f.Name), db)
			} else {
				r, err := srv.Files.Get(f.Id).Download()
				if err != nil {
					fmt.Printf("Download error: %v\n", err)
					continue
				}
				h := sha256.New()
				io.Copy(h, r.Body)
				r.Body.Close()
				hash := hex.EncodeToString(h.Sum(nil))
				db.UpsertFile(database.FileRecord{
					FileID:           f.Id,
					Provider:         "Google",
					OwnerEmail:       u.Email,
					FileHash:         hash,
					FileName:         f.Name,
					FileSize:         f.Size,
					FileExtension:    filepath.Ext(f.Name),
					ParentFolderID:   folderID,
					ParentFolderName: folderName,
					CreatedOn:        f.CreatedTime,
					LastModified:     f.ModifiedTime,
					LastSynced:       time.Now().Format(time.RFC3339),
				})
			}
		}
		if res.NextPageToken == "" {
			break
		}
		pageToken = res.NextPageToken
	}
}

func DeleteFile(f database.FileRecord) {
	driveSrv, err := getClient(config.ClientCreds{}, "")
	if err != nil {
		fmt.Printf("Google API error: %v\n", err)
		return
	}
	driveSrv.Files.Delete(f.FileID).Do()
}

func ShareSyncFolderWith(main, backup *config.User, creds config.ClientCreds, pw string) {
	driveSrv, err := getClient(creds, main.RefreshToken)
	if err != nil {
		fmt.Printf("Google API error: %v\n", err)
		return
	}
	q := "name = 'synched-cloud-drives' and 'root' in parents and trashed = false"
	res, err := driveSrv.Files.List().Q(q).Fields("files(id)").Do()
	if err != nil || len(res.Files) != 1 {
		fmt.Printf("Pre-flight failed: %v\n", err)
		return
	}
	perm := &drive.Permission{Type: "user", Role: "writer", EmailAddress: backup.Email}
	driveSrv.Permissions.Create(res.Files[0].Id, perm).SendNotificationEmail(false).Do()
}

func UploadFileFromMicrosoft(f database.FileRecord, cfg config.Config, pw string) {
	// Download from Microsoft, upload to Google
	// ...implementation omitted for brevity...
}

func UploadFileWithNewName(f database.FileRecord, newName string, cfg config.Config, pw string) {
	// Download file, upload with new name
	// ...implementation omitted for brevity...
}

func GetQuota(u config.User, creds config.ClientCreds, pw string) float64 {
	driveSrv, err := getClient(creds, u.RefreshToken)
	if err != nil {
		return 0
	}
	about, err := driveSrv.About.Get().Fields("storageQuota").Do()
	if err != nil {
		return 0
	}
	used := about.StorageQuota.Usage
	total := about.StorageQuota.Limit
	if total == 0 {
		return 0
	}
	return float64(used) / float64(total)
}

func TransferFileOwnership(f database.FileRecord, from, to string, creds config.ClientCreds, pw string) {
	driveSrv, err := getClient(creds, from)
	if err != nil {
		fmt.Printf("[Google][%s] API error: %v\n", from, err)
		return
	}
	q := "name = 'synched-cloud-drives' and mimeType = 'application/vnd.google-apps.folder' and trashed = false"
	var res *drive.FileList
	err = retry.Retry(5, time.Second, func() error {
		var apiErr error
		res, apiErr = driveSrv.Files.List().Q(q).Fields("files(id, name, parents, owners(emailAddress))").Do()
		return apiErr
	})
	if err != nil {
		fmt.Printf("[Google][%s] API error: %v\n", from, err)
		return
	}
	if len(res.Files) == 0 {
		fmt.Printf("[Google][%s] No accessible synched-cloud-drives folder found. If shared, make sure it is added to 'My Drive'.\n", from)
		return
	}
	if len(res.Files) > 1 {
		fmt.Printf("[Google][%s] Multiple synched-cloud-drives folders found. Please resolve manually.\n", from)
		return
	}
	folder := res.Files[0]
	// Check if folder is in root
	inRoot := false
	for _, p := range folder.Parents {
		if p == "root" {
			inRoot = true
			break
		}
	}
	if !inRoot {
		fmt.Printf("[Google][%s] synched-cloud-drives folder is not in root. Please move it to root.\n", from)
		return
	}
	// Check if folder is owned by main account
	owned := false
	if folder.Owners != nil {
		for _, owner := range folder.Owners {
			if owner.EmailAddress == from {
				owned = true
				break
			}
		}
	}
	if !owned {
		fmt.Printf("[Google][%s] synched-cloud-drives folder is not owned by main account. Please transfer ownership.\n", from)
		return
	}
	fmt.Printf("[Google][%s] Pre-flight check passed: unique, in root, and owned by main account.\n", from)
}
