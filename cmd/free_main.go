package cmd

import (
	"fmt"

	"cloud-drives-sync/database"
	"cloud-drives-sync/google"
	"cloud-drives-sync/microsoft"

	"github.com/spf13/cobra"
)

var freeMainCmd = &cobra.Command{
	Use:   "free-main",
	Short: "Transfer all files from main to backup accounts",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("[Free-Main] Running pre-flight check...")
		if !preFlightCheckAllAccounts() {
			fmt.Println("Pre-flight check failed. Aborting.")
			return
		}
		fmt.Println("[Free-Main] Transferring files from main to backup accounts...")
		mainAccounts := getMainAccounts()
		for _, main := range mainAccounts {
			files := getFilesOwnedByMain(main)
			for _, f := range files {
				backup := getBackupWithMostSpace(main.Provider)
				if backup == nil {
					fmt.Println("No backup account with enough space.")
					break
				}
				if !enoughSpace(*backup, f) {
					fmt.Printf("Not enough space in backup %s for file %s\n", backup.Email, f.FileName)
					continue
				}
				if canTransferOwnership(main, *backup) {
					transferOwnership(f, main, *backup)
					fmt.Printf("Transferred ownership of %s to %s\n", f.FileName, backup.Email)
				} else {
					downloadAndReupload(f, main, *backup)
					fmt.Printf("Re-uploaded %s to %s\n", f.FileName, backup.Email)
				}
			}
		}
		fmt.Println("[Free-Main] Done.")
	},
}

func init() {
	rootCmd.AddCommand(freeMainCmd)
}

func getMainAccounts() []struct{ Provider, Email string } {
	cfg, err := LoadConfig(promptForPassword())
	if err != nil || cfg == nil {
		fmt.Println("[Free-Main] Error loading config:", err)
		return nil
	}
	var mains []struct{ Provider, Email string }
	for _, u := range cfg.Users {
		if u.IsMain {
			mains = append(mains, struct{ Provider, Email string }{Provider: u.Provider, Email: u.Email})
		}
	}
	return mains
}

func getFilesOwnedByMain(main struct{ Provider, Email string }) []database.FileRecord {
	db := getDatabase()
	if db == nil {
		return nil
	}
	files, _ := db.GetAllFiles(main.Provider)
	var owned []database.FileRecord
	for _, f := range files {
		if f.OwnerEmail == main.Email {
			owned = append(owned, f)
		}
	}
	return owned
}

func enoughSpace(backup struct{ Provider, Email string }, f database.FileRecord) bool {
	cfg, _ := LoadConfig(promptForPassword())
	var used, total int64
	switch backup.Provider {
	case "Google":
		gd, _ := google.NewGoogleDrive(cfg.GoogleClient.ID, cfg.GoogleClient.Secret, getRefreshToken(cfg, backup.Provider, backup.Email))
		used, total, _ = gd.GetQuota(backup.Email)
	case "Microsoft":
		ms, _ := microsoft.NewOneDrive(cfg.MicrosoftClient.ID, cfg.MicrosoftClient.Secret, getRefreshToken(cfg, backup.Provider, backup.Email))
		used, total, _ = ms.GetQuota(backup.Email)
	}
	return (total - used) > f.FileSize
}

func canTransferOwnership(main, backup struct{ Provider, Email string }) bool {
	return main.Provider == backup.Provider
}

func transferOwnership(f database.FileRecord, main, backup struct{ Provider, Email string }) {
	switch main.Provider {
	case "Google":
		gd, _ := google.NewGoogleDrive("", "", "")
		_ = gd.TransferOwnership(f.FileID, main.Email, backup.Email)
	case "Microsoft":
		ms, _ := microsoft.NewOneDrive("", "", "")
		_ = ms.TransferOwnership(f.FileID, main.Email, backup.Email)
	}
}

func downloadAndReupload(f database.FileRecord, main, backup struct{ Provider, Email string }) {
	cfg, _ := LoadConfig(promptForPassword())
	var content []byte
	switch main.Provider {
	case "Google":
		gd, _ := google.NewGoogleDrive(cfg.GoogleClient.ID, cfg.GoogleClient.Secret, getRefreshToken(cfg, main.Provider, main.Email))
		content, _ = gd.DownloadFile(main.Email, f.FileID)
	case "Microsoft":
		ms, _ := microsoft.NewOneDrive(cfg.MicrosoftClient.ID, cfg.MicrosoftClient.Secret, getRefreshToken(cfg, main.Provider, main.Email))
		content, _ = ms.DownloadFile(main.Email, f.FileID)
	}
	switch backup.Provider {
	case "Google":
		gd, _ := google.NewGoogleDrive(cfg.GoogleClient.ID, cfg.GoogleClient.Secret, getRefreshToken(cfg, backup.Provider, backup.Email))
		_ = gd.UploadFile(backup.Email, f.FileName, content)
	case "Microsoft":
		ms, _ := microsoft.NewOneDrive(cfg.MicrosoftClient.ID, cfg.MicrosoftClient.Secret, getRefreshToken(cfg, backup.Provider, backup.Email))
		_ = ms.UploadFile(backup.Email, f.FileName, content)
	}
}
