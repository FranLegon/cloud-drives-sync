package cmd

import (
	"cloud-drives-sync/config"
	"cloud-drives-sync/database"
	"cloud-drives-sync/google"
	"cloud-drives-sync/microsoft"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var getMetadataCmd = &cobra.Command{
	Use:   "get-metadata",
	Short: "Scan and update local metadata for all files in synched-cloud-drives",
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

		for _, u := range cfg.Users {
			switch u.Provider {
			case "Google":
				if err := google.PreFlightCheck(u, cfg.GoogleClient, pw); err != nil {
					fmt.Printf("Pre-flight check failed for %s: %v\n", u.Email, err)
					os.Exit(1)
				}
			case "Microsoft":
				if err := microsoft.PreFlightCheck(u, cfg.MicrosoftClient, pw); err != nil {
					fmt.Printf("Pre-flight check failed for %s: %v\n", u.Email, err)
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
		fmt.Println("Metadata updated.")
	},
}

func init() {
	rootCmd.AddCommand(getMetadataCmd)
}
