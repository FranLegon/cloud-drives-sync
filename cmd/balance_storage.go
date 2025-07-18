package cmd

import (
	"cloud-drives-sync/database"
	"cloud-drives-sync/google"
	"cloud-drives-sync/microsoft"
	"fmt"

	"github.com/spf13/cobra"
)

var balanceStorageCmd = &cobra.Command{
	Use:   "balance-storage",
	Short: "Balance storage usage across backup accounts",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("[Balance-Storage] Running pre-flight check...")
		if !preFlightCheckAllAccounts() {
			fmt.Println("Pre-flight check failed. Aborting.")
			return
		}
		fmt.Println("[Balance-Storage] Checking quotas...")
		accounts := getAllAccounts()
		for _, acc := range accounts {
			used, total := getQuota(acc)
			if float64(used)/float64(total) > 0.95 {
				fmt.Printf("[Balance-Storage] %s is over 95%% full. Balancing...\n", acc.Email)
				files := getLargestFiles(acc)
				for _, f := range files {
					if !fileInOtherAccounts(f, acc) {
						backup := getBackupWithMostSpace(acc.Provider)
						if backup == nil {
							fmt.Println("No backup account with enough space.")
							break
						}
						transferOwnershipOrMove(f, acc, *backup)
						fmt.Printf("Moved %s to %s\n", f.FileName, backup.Email)
					}
					used2, total2 := getQuota(acc)
					if float64(used2)/float64(total2) < 0.90 {
						break
					}
				}
			}
		}
		fmt.Println("[Balance-Storage] Done.")
	},
}

func init() {
	rootCmd.AddCommand(balanceStorageCmd)
}

func getQuota(acc struct{ Provider, Email string }) (int64, int64) {
	cfg, _ := LoadConfig("")
	switch acc.Provider {
	case "Google":
		gd, _ := google.NewGoogleDrive(cfg.GoogleClient.ID, cfg.GoogleClient.Secret, getRefreshToken(cfg, acc.Provider, acc.Email))
		used, total, _ := gd.GetQuota(acc.Email)
		return used, total
	case "Microsoft":
		ms, _ := microsoft.NewOneDrive(cfg.MicrosoftClient.ID, cfg.MicrosoftClient.Secret, getRefreshToken(cfg, acc.Provider, acc.Email))
		used, total, _ := ms.GetQuota(acc.Email)
		return used, total
	}
	return 0, 0
}

func getLargestFiles(acc struct{ Provider, Email string }) []database.FileRecord {
	db := getDatabase()
	if db == nil {
		return nil
	}
	files, _ := db.GetLargestFiles(acc.Provider, acc.Email, 10)
	return files
}

func fileInOtherAccounts(f database.FileRecord, acc struct{ Provider, Email string }) bool {
	db := getDatabase()
	if db == nil {
		return false
	}
	files, _ := db.GetFilesByHash(f.FileHash)
	for _, file := range files {
		if file.Provider == acc.Provider && file.OwnerEmail != acc.Email {
			return true
		}
	}
	return false
}

func getBackupWithMostSpace(provider string) *struct{ Provider, Email string } {
	cfg, _ := LoadConfig("")
	var maxFree int64
	var backup struct{ Provider, Email string }
	found := false
	for _, u := range cfg.Users {
		if u.Provider == provider && !u.IsMain {
			var used, total int64
			switch provider {
			case "Google":
				gd, _ := google.NewGoogleDrive(cfg.GoogleClient.ID, cfg.GoogleClient.Secret, u.RefreshToken)
				used, total, _ = gd.GetQuota(u.Email)
			case "Microsoft":
				ms, _ := microsoft.NewOneDrive(cfg.MicrosoftClient.ID, cfg.MicrosoftClient.Secret, u.RefreshToken)
				used, total, _ = ms.GetQuota(u.Email)
			}
			free := total - used
			if !found || free > maxFree {
				maxFree = free
				backup = struct{ Provider, Email string }{Provider: provider, Email: u.Email}
				found = true
			}
		}
	}
	if !found {
		return nil
	}
	return &backup
}

func transferOwnershipOrMove(f database.FileRecord, from, to struct{ Provider, Email string }) {
	switch from.Provider {
	case "Google":
		gd, _ := google.NewGoogleDrive("", "", "")
		_ = gd.TransferOwnership(f.FileID, from.Email, to.Email)
	case "Microsoft":
		ms, _ := microsoft.NewOneDrive("", "", "")
		_ = ms.TransferOwnership(f.FileID, from.Email, to.Email)
	}
}
