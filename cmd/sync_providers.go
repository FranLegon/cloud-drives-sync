package cmd

import (
	"cloud-drives-sync/config"
	"cloud-drives-sync/database"
	"cloud-drives-sync/google"
	"cloud-drives-sync/microsoft"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
)

var syncProvidersCmd = &cobra.Command{
	Use:   "sync-providers",
	Short: "Mirror files between main Google and Microsoft accounts",
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
			main := cfg.GetMainAccount(prov)
			if main == nil {
				fmt.Printf("No main %s account found.\n", prov)
				os.Exit(1)
			}
			if prov == "Google" {
				if err := google.PreFlightCheck(*main, cfg.GoogleClient, pw); err != nil {
					fmt.Printf("Pre-flight check failed for %s: %v\n", main.Email, err)
					os.Exit(1)
				}
			} else {
				if err := microsoft.PreFlightCheck(*main, cfg.MicrosoftClient, pw); err != nil {
					fmt.Printf("Pre-flight check failed for %s: %v\n", main.Email, err)
					os.Exit(1)
				}
			}
		}

		for _, u := range cfg.Users {
			switch u.Provider {
			case "Google":
				google.ScanAndUpdateMetadata(u, cfg.GoogleClient, pw, db)
			case "Microsoft":
				microsoft.ScanAndUpdateMetadata(u, cfg.MicrosoftClient, pw, db)
			}
		}

		googleFiles, _ := db.GetFilesByProvider("Google", cfg.GetMainAccount("Google").Email)
		msFiles, _ := db.GetFilesByProvider("Microsoft", cfg.GetMainAccount("Microsoft").Email)

		googleHashes := map[string]database.FileRecord{}
		msHashes := map[string]database.FileRecord{}
		for _, f := range googleFiles {
			googleHashes[f.FileHash] = f
		}
		for _, f := range msFiles {
			msHashes[f.FileHash] = f
		}

		for hash, gfile := range googleHashes {
			if _, ok := msHashes[hash]; !ok {
				fmt.Printf("Uploading %s to Microsoft...\n", gfile.FileName)
				if safeMode {
					SafeLog("UPLOAD %s to Microsoft at %s", gfile.FileName, gfile.FileName)
				} else {
					microsoft.UploadFileFromGoogle(gfile, cfg, pw)
				}
			}
		}
		for hash, mfile := range msHashes {
			if _, ok := googleHashes[hash]; !ok {
				fmt.Printf("Uploading %s to Google...\n", mfile.FileName)
				if safeMode {
					SafeLog("UPLOAD %s to Google at %s", mfile.FileName, mfile.FileName)
				} else {
					google.UploadFileFromMicrosoft(mfile, cfg, pw)
				}
			}
		}
		for _, gfile := range googleFiles {
			for _, mfile := range msFiles {
				if gfile.FileName == mfile.FileName && gfile.FileHash != mfile.FileHash && gfile.ParentFolderName == mfile.ParentFolderName {
					conflictName := fmt.Sprintf("%s_conflict_%s%s", gfile.FileName[:len(gfile.FileName)-len(gfile.FileExtension)], time.Now().Format("2006-01-02"), gfile.FileExtension)
					fmt.Printf("Conflict: %s exists with different hashes. Renaming and uploading as %s\n", gfile.FileName, conflictName)
					if safeMode {
						SafeLog("UPLOAD conflict file %s as %s", gfile.FileName, conflictName)
					} else {
						google.UploadFileWithNewName(gfile, conflictName, cfg, pw)
					}
				}
			}
		}
		fmt.Println("Sync complete.")
	},
}

func init() {
	rootCmd.AddCommand(syncProvidersCmd)
}
