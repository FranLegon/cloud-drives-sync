package cmd

import (
	"cloud-drives-sync/config"
	"cloud-drives-sync/database"
	"cloud-drives-sync/google"
	"cloud-drives-sync/microsoft"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
)

var freeMainCmd = &cobra.Command{
	Use:   "free-main",
	Short: "Transfer all files in synched-cloud-drives owned by main account to backup account with most free space.",
	Run: func(cmd *cobra.Command, args []string) {
		pw, cfg, db := getConfigAndDB()
		for _, provider := range []string{"Google", "Microsoft"} {
			main, backups := getMainAndBackups(cfg, provider)
			if main == nil || len(backups) == 0 {
				fmt.Printf("No main or backup accounts for %s.\n", provider)
				continue
			}
			freeMainForProvider(provider, main, backups, cfg, pw, db)
		}
	},
}

func init() {
	rootCmd.AddCommand(freeMainCmd)
}

func freeMainForProvider(provider string, main *config.User, backups []*config.User, cfg *config.Config, pw string, db database.DatabaseInterface) {
	// Pre-flight check
	if err := preFlightCheckProvider(provider, main, backups, cfg, pw); err != nil {
		fmt.Println("Pre-flight check failed:", err)
		os.Exit(1)
	}
	// Get all files owned by main in synched-cloud-drives
	files := db.GetFilesByOwner(main.Email, provider)
	var filtered []database.FileRecord
	for _, f := range files {
		if f.ParentFolderName == "synched-cloud-drives" {
			filtered = append(filtered, f)
		}
	}
	files = filtered
	if len(files) == 0 {
		fmt.Println("No files to transfer for", provider)
		return
	}
	// Get free space for each backup
	backupSpace := make(map[string]float64)
	for _, b := range backups {
		if provider == "Google" {
			backupSpace[b.Email] = google.GetQuota(*b, cfg.GoogleClient, pw)
		} else {
			backupSpace[b.Email] = microsoft.GetQuota(*b, cfg.MicrosoftClient, pw)
		}
	}
	for _, f := range files {
		// Find backup with most free space
		var best *config.User
		maxFree := float64(0)
		for _, b := range backups {
			if backupSpace[b.Email] > maxFree {
				maxFree = backupSpace[b.Email]
				best = b
			}
		}
		if best == nil || maxFree*1000000000 < float64(f.FileSize) { // Quota is fraction, FileSize is bytes
			fmt.Printf("Not enough space to transfer %s (%d bytes) to any backup account.\n", f.FileName, f.FileSize)
			continue
		}
		// Download and reupload file with metadata
		if provider == "Google" {
			transferGoogleFileToBackup(f, main, best, cfg, pw, db)
		} else {
			transferMicrosoftFileToBackup(f, main, best, cfg, pw, db)
		}
		// Update backupSpace after transfer
		backupSpace[best.Email] -= float64(f.FileSize) / 1000000000
	}
}

func transferGoogleFileToBackup(f database.FileRecord, main, backup *config.User, cfg *config.Config, pw string, db database.DatabaseInterface) {
	// Download from main, upload to backup
	reader := google.DownloadFile(f, *main, cfg.GoogleClient, pw)
	newID := google.UploadFile(reader, f, *backup, cfg.GoogleClient, pw)
	f.FileID = newID
	f.OwnerEmail = backup.Email
	f.LastSynced = time.Now().Format(time.RFC3339)
	db.UpsertFile(f)
	google.DeleteFile(f)
	fmt.Printf("Transferred %s to backup %s (Google)\n", f.FileName, backup.Email)
}

func transferMicrosoftFileToBackup(f database.FileRecord, main, backup *config.User, cfg *config.Config, pw string, db database.DatabaseInterface) {
	reader := microsoft.DownloadFile(f, *main, cfg.MicrosoftClient, pw)
	newID := microsoft.UploadFile(reader, f, *backup, cfg.MicrosoftClient, pw)
	f.FileID = newID
	f.OwnerEmail = backup.Email
	f.LastSynced = time.Now().Format(time.RFC3339)
	db.UpsertFile(f)
	microsoft.DeleteFile(f)
	fmt.Printf("Transferred %s to backup %s (Microsoft)\n", f.FileName, backup.Email)
}

func getMainAndBackups(cfg *config.Config, provider string) (*config.User, []*config.User) {
	var main *config.User
	var backups []*config.User
	for i := range cfg.Users {
		if cfg.Users[i].Provider == provider {
			if cfg.Users[i].IsMain {
				main = &cfg.Users[i]
			} else {
				backups = append(backups, &cfg.Users[i])
			}
		}
	}
	return main, backups
}

func preFlightCheckProvider(provider string, main *config.User, backups []*config.User, cfg *config.Config, pw string) error {
	if provider == "Google" {
		if err := google.PreFlightCheck(*main, cfg.GoogleClient, pw); err != nil {
			return err
		}
		for _, b := range backups {
			if err := google.PreFlightCheck(*b, cfg.GoogleClient, pw); err != nil {
				return err
			}
		}
	} else {
		if err := microsoft.PreFlightCheck(*main, cfg.MicrosoftClient, pw); err != nil {
			return err
		}
		for _, b := range backups {
			if err := microsoft.PreFlightCheck(*b, cfg.MicrosoftClient, pw); err != nil {
				return err
			}
		}
	}
	return nil
}

// Removed nowRFC3339, use time.Now().Format(time.RFC3339) instead

func getConfigAndDB() (string, *config.Config, database.DatabaseInterface) {
	// ...existing code to load config and db...
	return "", nil, nil // Replace with actual implementation
}
