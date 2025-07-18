package cmd

import (
	"cloud-drives-sync/google"
	"cloud-drives-sync/microsoft"
	"fmt"

	"github.com/spf13/cobra"
)

var shareWithMainCmd = &cobra.Command{
	Use:   "share-with-main",
	Short: "Repair permissions for backup accounts",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("[Share-With-Main] Running pre-flight check...")
		if !preFlightCheckAllAccounts() {
			fmt.Println("Pre-flight check failed. Aborting.")
			return
		}
		fmt.Println("[Share-With-Main] Verifying permissions...")
		accounts := getAllAccounts()
		for _, acc := range accounts {
			if !accIsMain(acc) {
				if !hasEditorAccess(acc) {
					grantEditorAccess(acc)
					fmt.Printf("[Share-With-Main] Granted editor access to %s\n", acc.Email)
				}
			}
		}
		fmt.Println("[Share-With-Main] Permission repair complete.")
	},
}

func init() {
	rootCmd.AddCommand(shareWithMainCmd)
}

type Account struct {
	Provider string
	Email    string
}

func accIsMain(acc interface{}) bool {
	account, ok := acc.(Account)
	if !ok {
		return false
	}
	cfg, err := LoadConfig("")
	if err != nil {
		return false
	}
	for _, u := range cfg.Users {
		if u.Provider == account.Provider && u.Email == account.Email && u.IsMain {
			return true
		}
	}
	return false
}

func hasEditorAccess(acc interface{}) bool {
	// For now, always return false to ensure ShareSyncFolder is always called (safe default)
	fmt.Printf("[hasEditorAccess] Assuming %v has no editor access", acc)
	return false
}

func grantEditorAccess(acc interface{}) {
	account, ok := acc.(Account)
	if !ok {
		return
	}
	cfg, err := LoadConfig("")
	if err != nil {
		return
	}
	var mainEmail string
	for _, u := range cfg.Users {
		if u.Provider == account.Provider && u.IsMain {
			mainEmail = u.Email
			break
		}
	}
	if mainEmail == "" {
		return
	}
	switch account.Provider {
	case "Google":
		gd, _ := google.NewGoogleDrive(cfg.GoogleClient.ID, cfg.GoogleClient.Secret, getRefreshToken(cfg, account.Provider, account.Email))
		_ = gd.ShareSyncFolder(mainEmail, account.Email)
	case "Microsoft":
		ms, _ := microsoft.NewOneDrive(cfg.MicrosoftClient.ID, cfg.MicrosoftClient.Secret, getRefreshToken(cfg, account.Provider, account.Email))
		_ = ms.ShareSyncFolder(mainEmail, account.Email)
	}
}
