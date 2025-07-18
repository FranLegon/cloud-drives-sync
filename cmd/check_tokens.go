package cmd

import (
	"fmt"

	"cloud-drives-sync/google"
	"cloud-drives-sync/microsoft"

	"github.com/spf13/cobra"
)

var checkTokensCmd = &cobra.Command{
	Use:   "check-tokens",
	Short: "Check validity of all refresh tokens",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("[Check-Tokens] Checking all refresh tokens...")
		accounts := getAllAccounts()
		for _, acc := range accounts {
			if !tokenIsValid(acc) {
				fmt.Printf("[Check-Tokens] Token invalid for %s. Please re-authenticate using add-account.\n", acc.Email)
			}
		}
		fmt.Println("[Check-Tokens] Done.")
	},
}

func init() {
	rootCmd.AddCommand(checkTokensCmd)
}

// Helper stub
func tokenIsValid(acc struct{ Provider, Email string }) bool {
	cfg, _ := LoadConfig("")
	switch acc.Provider {
	case "Google":
		gd, _ := google.NewGoogleDrive(cfg.GoogleClient.ID, cfg.GoogleClient.Secret, getRefreshToken(cfg, acc.Provider, acc.Email))
		return gd.CheckToken(acc.Email) == nil
	case "Microsoft":
		ms, _ := microsoft.NewOneDrive(cfg.MicrosoftClient.ID, cfg.MicrosoftClient.Secret, getRefreshToken(cfg, acc.Provider, acc.Email))
		return ms.CheckToken(acc.Email) == nil
	}
	return false
}
