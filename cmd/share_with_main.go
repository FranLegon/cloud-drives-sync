package cmd

import (
	"cloud-drives-sync/config"
	"cloud-drives-sync/google"
	"cloud-drives-sync/microsoft"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var shareWithMainCmd = &cobra.Command{
	Use:   "share-with-main",
	Short: "Repair permissions: ensure all backup accounts have editor access to main's synched-cloud-drives",
	Run: func(cmd *cobra.Command, args []string) {
		exeDir, _ := os.Executable()
		exeDir = filepath.Dir(exeDir)
		configPath := filepath.Join(exeDir, "config.json.enc")
		cfg, pw, err := config.LoadConfigWithPassword(configPath)
		if err != nil {
			fmt.Printf("Failed to load config: %v\n", err)
			os.Exit(1)
		}
		for _, prov := range []string{"Google", "Microsoft"} {
			main := cfg.GetMainAccount(prov)
			if main == nil {
				continue
			}
			for _, u := range cfg.Users {
				if u.Provider == prov && !u.IsMain {
					if prov == "Google" {
						google.ShareSyncFolderWith(main, &u, cfg.GoogleClient, pw)
					} else {
						microsoft.ShareSyncFolderWith(main, &u, cfg.MicrosoftClient, pw)
					}
				}
			}
		}
		fmt.Println("Permissions repaired.")
	},
}

func init() {
	rootCmd.AddCommand(shareWithMainCmd)
}
