package cmd

import (
	"cloud-drives-sync/config"
	"cloud-drives-sync/database"
	"cloud-drives-sync/google"
	"cloud-drives-sync/microsoft"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"
)

var balanceStorageCmd = &cobra.Command{
	Use:   "balance-storage",
	Short: "Move files to backup accounts to keep storage below 90%",
	Run: func(cmd *cobra.Command, args []string) {
		exeDir, _ := os.Executable()
		exeDir = filepath.Dir(exeDir)
		configPath := filepath.Join(exeDir, "config.json.enc")
		dbPath := filepath.Join(exeDir, "metadata.db")
		cfg, pw, err := config.LoadConfigWithPassword(configPath)
		if err != nil {
			fmt.Printf("Failed to load config: %v\n", err)
			os.Exit(1)
		}
		db, err := database.OpenDB(dbPath)
		if err != nil {
			fmt.Printf("Failed to open DB: %v\n", err)
			os.Exit(1)
		}
		defer db.Close()

		for _, prov := range []string{"Google", "Microsoft"} {
			accounts := cfg.GetAccounts(prov)
			if len(accounts) < 2 {
				continue
			}
			quota := map[string]float64{}
			for _, u := range accounts {
				if prov == "Google" {
					quota[u.Email] = google.GetQuota(u, cfg.GoogleClient, pw)
				} else {
					quota[u.Email] = microsoft.GetQuota(u, cfg.MicrosoftClient, pw)
				}
			}
			for email, used := range quota {
				if used > 0.95 {
					fmt.Printf("%s is over 95%% full.\n", email)
					files, _ := db.GetLargestFilesNotInOtherAccounts(prov, email)
					sort.Slice(files, func(i, j int) bool { return files[i].FileSize > files[j].FileSize })
					for _, f := range files {
						target := ""
						maxFree := 0.0
						for e, q := range quota {
							if e != email && q < 0.90 && q > maxFree {
								target = e
								maxFree = q
							}
						}
						if target == "" {
							fmt.Println("No suitable backup account found.")
							break
						}
						if safeMode {
							SafeLog("MOVE %s from %s to %s", f.FileName, email, target)
						} else {
							if prov == "Google" {
								google.TransferFileOwnership(f, email, target, cfg.GoogleClient, pw)
							} else {
								microsoft.TransferFileOwnership(f, email, target, cfg.MicrosoftClient, pw)
							}
							db.UpdateOwnerEmail(f.FileID, prov, target)
							quota[email] -= float64(f.FileSize)
							quota[target] += float64(f.FileSize)
							if quota[email] < 0.90 {
								break
							}
						}
					}
				}
			}
		}
		fmt.Println("Storage balanced.")
	},
}

func init() {
	rootCmd.AddCommand(balanceStorageCmd)
}
