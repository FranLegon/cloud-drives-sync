package cmd

import (
	"cloud-drives-sync/database"
	"cloud-drives-sync/google"
	"cloud-drives-sync/microsoft"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
)

var getMetadataCmd = &cobra.Command{
	Use:   "get-metadata",
	Short: "Scan and update local metadata from all cloud accounts",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("[Get-Metadata] Running pre-flight check...")
		if !preFlightCheckAllAccounts() {
			fmt.Println("Pre-flight check failed. Aborting.")
			os.Exit(1)
		}
		fmt.Println("[Get-Metadata] Scanning files in synched-cloud-drives folders...")
		accounts := getAllAccounts()
		db := getDatabase()
		for _, acc := range accounts {
			files := listFilesInSyncFolder(acc)
			for _, f := range files {
				hash := calculateSHA256(f)
				fileRecord := database.FileRecord{
					FileID:           f.ID,
					Provider:         acc.Provider,
					OwnerEmail:       acc.Email,
					FileHash:         hash,
					FileName:         f.Name,
					FileSize:         f.Size,
					FileExtension:    getFileExtension(f.Name),
					ParentFolderID:   f.ParentID,
					ParentFolderName: "synched-cloud-drives",
					CreatedOn:        f.Created,
					LastModified:     f.Modified,
					LastSynced:       getCurrentTimestamp(),
				}
				db.InsertOrUpdateFile(fileRecord)
				fmt.Printf("[Account: %s] Synced file: %s\n", acc.Email, f.Name)
			}
		}
		fmt.Println("[Get-Metadata] Metadata update complete.")
	},
}

func init() {
	rootCmd.AddCommand(getMetadataCmd)
}

// Helper stubs for integration
func preFlightCheckAllAccounts() bool {
	cfg, err := LoadConfig(promptForPassword())
	if err != nil {
		fmt.Println("[Get-Metadata] Failed to load config:", err)
		return false
	}
	for _, u := range cfg.Users {
		switch u.Provider {
		case "Google":
			gd, _ := google.NewGoogleDrive(cfg.GoogleClient.ID, cfg.GoogleClient.Secret, u.RefreshToken)
			if err := gd.PreFlightCheck(u.Email); err != nil {
				fmt.Println(err)
				return false
			}
		case "Microsoft":
			ms, _ := microsoft.NewOneDrive(cfg.MicrosoftClient.ID, cfg.MicrosoftClient.Secret, u.RefreshToken)
			if err := ms.PreFlightCheck(u.Email); err != nil {
				fmt.Println(err)
				return false
			}
		}
	}
	return true
}

func getAllAccounts() []struct{ Provider, Email string } {
	cfg, err := LoadConfig(promptForPassword())
	if err != nil {
		fmt.Println("[Get-Metadata] Failed to load config:", err)
		return nil
	}
	var accounts []struct{ Provider, Email string }
	for _, u := range cfg.Users {
		accounts = append(accounts, struct{ Provider, Email string }{Provider: u.Provider, Email: u.Email})
	}
	return accounts
}

func getDatabase() database.Database {
	db := &database.SQLiteDB{}
	err := db.InitDB("bin/metadata.db")
	if err != nil {
		fmt.Println("[Database] Failed to initialize DB:", err)
		return nil
	}
	return db
}
func listFilesInSyncFolder(acc struct{ Provider, Email string }) []struct {
	ID, Name, ParentID, Created, Modified string
	Size                                  int64
} {
	cfg, _ := LoadConfig(promptForPassword())
	switch acc.Provider {
	case "Google":
		gd, _ := google.NewGoogleDrive(cfg.GoogleClient.ID, cfg.GoogleClient.Secret, getRefreshToken(cfg, acc.Provider, acc.Email))
		files, _ := gd.ListFilesInSyncFolder(acc.Email)
		var out []struct {
			ID, Name, ParentID, Created, Modified string
			Size                                  int64
		}
		for _, f := range files {
			out = append(out, struct {
				ID, Name, ParentID, Created, Modified string
				Size                                  int64
			}{ID: f.ID, Name: f.Name, ParentID: f.ParentID, Created: f.Created, Modified: f.Modified, Size: f.Size})
		}
		return out
	case "Microsoft":
		ms, _ := microsoft.NewOneDrive(cfg.MicrosoftClient.ID, cfg.MicrosoftClient.Secret, getRefreshToken(cfg, acc.Provider, acc.Email))
		files, _ := ms.ListFilesInSyncFolder(acc.Email)
		var out []struct {
			ID, Name, ParentID, Created, Modified string
			Size                                  int64
		}
		for _, f := range files {
			out = append(out, struct {
				ID, Name, ParentID, Created, Modified string
				Size                                  int64
			}{ID: f.ID, Name: f.Name, ParentID: f.ParentID, Created: f.Created, Modified: f.Modified, Size: f.Size})
		}
		return out
	}
	return nil
}

func getRefreshToken(cfg *Config, provider, email string) string {
	for _, u := range cfg.Users {
		if u.Provider == provider && u.Email == email {
			return u.RefreshToken
		}
	}
	return ""
}

func calculateSHA256(f struct {
	ID, Name, ParentID, Created, Modified string
	Size                                  int64
}) string {
	// In production, download the file and hash its content
	// Here, just hash the ID+Name+Size+Created+Modified for demonstration
	data := f.ID + f.Name + fmt.Sprint(f.Size) + f.Created + f.Modified
	h := sha256.Sum256([]byte(data))
	return hex.EncodeToString(h[:])
}

func getFileExtension(name string) string {
	return filepath.Ext(name)
}

func getCurrentTimestamp() string {
	return time.Now().UTC().Format(time.RFC3339)
}
