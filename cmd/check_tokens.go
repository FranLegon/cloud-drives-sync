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

var checkTokensCmd = &cobra.Command{
	Use:   "check-tokens",
	Short: "Check validity of all refresh tokens",
	Run: func(cmd *cobra.Command, args []string) {
		exeDir, _ := os.Executable()
		exeDir = filepath.Dir(exeDir)
		configPath := filepath.Join(exeDir, "config.json.enc")
		cfg, pw, err := config.LoadConfigWithPassword(configPath)
		if err != nil {
			fmt.Printf("Failed to load config: %v\n", err)
			os.Exit(1)
		}
		for _, u := range cfg.Users {
			var ok bool
			if u.Provider == "Google" {
				ok = google.CheckToken(u, cfg.GoogleClient, pw)
			} else {
				ok = microsoft.CheckToken(u, cfg.MicrosoftClient, pw)
			}
			if !ok {
				fmt.Printf("Token for %s (%s) is invalid. Please re-authenticate with add-account.\n", u.Email, u.Provider)
			} else {
				fmt.Printf("Token for %s (%s) is valid.\n", u.Email, u.Provider)
			}
		}
	},
}

func init() {
	rootCmd.AddCommand(checkTokensCmd)
}
