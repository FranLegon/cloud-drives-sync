package cmd

import (
	"cloud-drives-sync/internal/auth"
	"cloud-drives-sync/internal/logger"
	"cloud-drives-sync/internal/model"
	"fmt"

	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
)

var addAccountCmd = &cobra.Command{
	Use:   "add-account",
	Short: "Adds a backup account to a provider.",
	Long: `Adds a new backup account for a provider that already has a main account.
This command will:
1. Initiate an OAuth flow to authorize the new backup account.
2. Grant the backup account 'editor' access to the main account's 'synched-cloud-drives' folder.`,
	Run: func(cmd *cobra.Command, args []string) {
		runner := setup() // This loads the existing config and clients
		cfg := runner.Config

		prompt := promptui.Select{
			Label: "Select provider for the new Backup Account",
			Items: []string{"Google", "Microsoft"},
		}
		_, provider, err := prompt.Run()
		if err != nil {
			logger.Error(err, "Provider selection failed")
		}

		var mainUser model.User
		foundMain := false
		for _, u := range cfg.Users {
			if u.IsMain && u.Provider == provider {
				mainUser = u
				foundMain = true
				break
			}
		}
		if !foundMain {
			logger.Error(fmt.Errorf("no main account found for %s", provider), "Cannot add a backup account until a main account is configured with 'init'.")
		}

		logger.Info("Please authorize the new backup account...")
		refreshToken, err := auth.StartOAuthFlow(provider, cfg)
		if err != nil {
			logger.Error(err, "OAuth flow failed for backup account")
		}

		backupUserEmail, err := getEmailFromToken(provider, cfg, refreshToken)
		if err != nil {
			logger.Error(err, "Could not verify new backup account's email")
		}

		for _, u := range cfg.Users {
			if u.Email == backupUserEmail {
				logger.Error(nil, "Account %s is already configured.", backupUserEmail)
			}
		}

		newUser := model.User{
			Provider:     provider,
			Email:        backupUserEmail,
			IsMain:       false,
			RefreshToken: refreshToken,
		}
		cfg.Users = append(cfg.Users, newUser)

		mainClient := runner.Clients[mainUser.Email]
		syncFolderID, err := mainClient.PreFlightCheck()
		if err != nil || syncFolderID == "" {
			logger.Error(err, "Could not find or access the sync folder for main account %s", mainUser.Email)
		}

		logger.Info("Sharing main sync folder with %s...", backupUserEmail)
		if _, err := mainClient.Share(syncFolderID, backupUserEmail); err != nil {
			logger.Error(err, "Failed to share sync folder")
		}

		password := promptForPassword("Confirm Master Password to save changes")
		if err := cfg.SaveConfig(password); err != nil {
			logger.Error(err, "Failed to save configuration")
		}
		logger.Info("Successfully added and configured backup account %s.", backupUserEmail)
	},
}
