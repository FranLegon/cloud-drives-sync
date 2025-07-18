package cmd

import (
	gdrive "cloud-drives-sync/google"
	msdrive "cloud-drives-sync/microsoft"
	"fmt"

	"github.com/spf13/cobra"
)

var syncProvidersCmd = &cobra.Command{
	Use:   "sync-providers",
	Short: "Synchronize file content between main Google and Microsoft accounts",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("[Sync-Providers] Ensuring metadata is current...")
		getMetadataCmd.Run(cmd, args)
		fmt.Println("[Sync-Providers] Comparing file hashes across providers...")
		googleFiles := getFilesByProvider("Google")
		microsoftFiles := getFilesByProvider("Microsoft")
		googleHashes := getHashes(googleFiles)
		microsoftHashes := getHashes(microsoftFiles)
		for hash, gFile := range googleHashes {
			if _, exists := microsoftHashes[hash]; !exists {
				uploadFileToProvider("Microsoft", gFile)
				fmt.Printf("Uploaded %s to Microsoft\n", gFile.FileName)
			}
		}
		for hash, mFile := range microsoftHashes {
			if _, exists := googleHashes[hash]; !exists {
				uploadFileToProvider("Google", mFile)
				fmt.Printf("Uploaded %s to Google\n", mFile.FileName)
			}
		}
		fmt.Println("[Sync-Providers] Sync complete.")
	},
}

func init() {
	rootCmd.AddCommand(syncProvidersCmd)
}

// Helper functions
type SyncFile struct {
	FileName   string
	FileHash   string
	Provider   string
	OwnerEmail string
	FileID     string
}

func getFilesByProvider(provider string) []SyncFile {
	db := getDatabase()
	if db == nil {
		return nil
	}
	records, err := db.GetAllFiles(provider)
	if err != nil {
		return nil
	}
	var files []SyncFile
	for _, r := range records {
		files = append(files, SyncFile{
			FileName:   r.FileName,
			FileHash:   r.FileHash,
			Provider:   r.Provider,
			OwnerEmail: r.OwnerEmail,
			FileID:     r.FileID,
		})
	}
	return files
}

func getHashes(files []SyncFile) map[string]SyncFile {
	hashMap := make(map[string]SyncFile)
	for _, f := range files {
		if f.FileHash != "" {
			hashMap[f.FileHash] = f
		}
	}
	return hashMap
}

func uploadFileToProvider(provider string, file SyncFile) {
	cfg, _ := LoadConfig(promptForPassword())
	var content []byte
	var err error
	switch file.Provider {
	case "Google":
		gd, _ := getGoogleDrive(cfg, file.OwnerEmail)
		content, err = gd.DownloadFile(file.OwnerEmail, file.FileID)
	case "Microsoft":
		ms, _ := getOneDrive(cfg, file.OwnerEmail)
		content, err = ms.DownloadFile(file.OwnerEmail, file.FileID)
	}
	if err != nil {
		fmt.Printf("[Sync-Providers] Error downloading %s: %v\n", file.FileName, err)
		return
	}
	switch provider {
	case "Google":
		main := getMainAccount(cfg, "Google")
		gd, _ := getGoogleDrive(cfg, main.Email)
		err = gd.UploadFile(main.Email, file.FileName, content)
	case "Microsoft":
		main := getMainAccount(cfg, "Microsoft")
		ms, _ := getOneDrive(cfg, main.Email)
		err = ms.UploadFile(main.Email, file.FileName, content)
	}
	if err != nil {
		fmt.Printf("[Sync-Providers] Error uploading %s: %v\n", file.FileName, err)
	}
}

func promptForPassword() string {
	var pw string
	fmt.Print("Enter master password: ")
	fmt.Scanln(&pw)
	return pw
}

func getGoogleDrive(cfg *Config, email string) (gdrive.GoogleDrive, error) {
	for _, u := range cfg.Users {
		if u.Provider == "Google" && u.Email == email {
			return gdrive.NewGoogleDrive(cfg.GoogleClient.ID, cfg.GoogleClient.Secret, u.RefreshToken)
		}
	}
	return nil, fmt.Errorf("[getGoogleDrive] Google account not found: %s", email)
}

func getOneDrive(cfg *Config, email string) (msdrive.OneDrive, error) {
	for _, u := range cfg.Users {
		if u.Provider == "Microsoft" && u.Email == email {
			return msdrive.NewOneDrive(cfg.MicrosoftClient.ID, cfg.MicrosoftClient.Secret, u.RefreshToken)
		}
	}
	return nil, fmt.Errorf("[getOneDrive] Microsoft account not found: %s", email)
}

func getMainAccount(cfg *Config, provider string) struct{ Email string } {
	for _, u := range cfg.Users {
		if u.Provider == provider && u.IsMain {
			return struct{ Email string }{Email: u.Email}
		}
	}
	return struct{ Email string }{Email: ""}
}
