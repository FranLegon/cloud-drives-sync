package cmd

import (
	"context"

	"cloud-drives-sync/internal/config"
	"cloud-drives-sync/internal/logger"
	"cloud-drives-sync/internal/model"

	"github.com/spf13/cobra"
)

// shareWithMainCmd represents the share-with-main command
var shareWithMainCmd = &cobra.Command{
	Use:   "share-with-main",
	Short: "Repairs permissions by sharing the main sync folder with all backup accounts",
	Long: `This is a utility command to ensure correct permissions. For each provider
(Google and Microsoft), it finds the main account's 'synched-cloud-drives' folder.

It then iterates through all configured backup accounts for that same provider and
verifies that each one has 'editor' (write) access to that folder, re-applying the
permission if necessary. This is useful for repairing accidentally removed permissions
or for ensuring a consistent state.`,
	Run: runShareWithMain,
}

func runShareWithMain(cmd *cobra.Command, args []string) {
	logger.Info("Verifying and repairing sync folder permissions...")

	// --- Step 1: Load Config and Get Password ---
	masterPassword, err := config.GetMasterPassword(false)
	if err != nil {
		logger.Fatal("Failed to get master password: %v", err)
	}
	appCfg, err := config.LoadConfig(masterPassword)
	if err != nil {
		logger.Fatal("Failed to load config: %v", err)
	}

	// --- Step 2: Group Accounts by Provider ---
	accountsByProvider := make(map[string][]model.User)
	for _, user := range appCfg.Users {
		accountsByProvider[user.Provider] = append(accountsByProvider[user.Provider], user)
	}

	ctx := context.Background()

	// --- Step 3: Iterate Through Each Provider ---
	for provider, accounts := range accountsByProvider {
		logger.Info("\n--- Checking Provider: %s ---", provider)

		// Find main and backup accounts for this provider
		var mainUser *model.User
		var backupUsers []model.User
		for i, acc := range accounts {
			if acc.IsMain {
				mainUser = &accounts[i]
			} else {
				backupUsers = append(backupUsers, acc)
			}
		}

		if mainUser == nil {
			logger.Info("No main account configured for this provider. Skipping.")
			continue
		}
		if len(backupUsers) == 0 {
			logger.Info("No backup accounts configured for this provider. Nothing to do.")
			continue
		}

		// --- Step 4: Get Main Client and Sync Folder ID ---
		mainClient, err := getClientForUser(ctx, mainUser, appCfg)
		if err != nil {
			logger.TaggedError(mainUser.Email, "Could not create API client. Skipping this provider. Error: %v", err)
			continue
		}

		syncFolderID, err := mainClient.PreflightCheck(ctx)
		if err != nil {
			logger.TaggedError(mainUser.Email, "Could not find or verify the sync folder. Skipping this provider. Error: %v", err)
			continue
		}
		logger.Info("Found sync folder for main account %s (ID: %s)", mainUser.Email, syncFolderID)

		// --- Step 5: Share Folder with Each Backup Account ---
		for _, backupUser := range backupUsers {
			logger.Info("Ensuring '%s' has editor access to the sync folder...", backupUser.Email)

			if safeRun {
				logger.TaggedInfo("DRY RUN", "Would share folder %s from %s with %s", syncFolderID, mainUser.Email, backupUser.Email)
				continue
			}

			err := mainClient.ShareFolder(ctx, syncFolderID, backupUser.Email)
			if err != nil {
				// The API call might fail for various reasons (e.g., policy restrictions),
				// but we log it and continue to the next user.
				logger.Error("Failed to apply permission for %s. Error: %v", backupUser.Email, err)
			} else {
				logger.Info("Permission successfully verified/applied for %s.", backupUser.Email)
			}
		}
	}

	logger.Info("\nPermission check and repair process complete.")
}
