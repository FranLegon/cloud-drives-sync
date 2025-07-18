package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/api"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/auth"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/config"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/google"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/logger"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/microsoft"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/model"
	"golang.org/x/oauth2"
)

// addAccountCmd represents the add-account command
var addAccountCmd = &cobra.Command{
	Use:   "add-account",
	Short: "Adds a new backup account to a configured provider",
	Long: `This command adds a new non-main (backup) account to a provider that already
has a main account configured. It initiates the same OAuth 2.0 flow as the 'init' command.

Upon successful authorization, the main account for that provider will automatically
share its 'synched-cloud-drives' folder with the newly added backup account, granting
it 'editor' permissions. This allows the backup account to be used for storage balancing
and data redundancy.`,
	Run: runAddAccount,
}

func runAddAccount(cmd *cobra.Command, args []string) {
	ctx := context.Background()

	// --- Step 1: Load Config and Get Password ---
	masterPassword, err := config.GetMasterPassword(false)
	if err != nil {
		logger.Fatal("Failed to get master password: %v", err)
	}
	appCfg, err := config.LoadConfig(masterPassword)
	if err != nil {
		logger.Fatal("Failed to load config: %v", err)
	}

	// --- Step 2: Select Provider ---
	providerPrompt := promptui.Select{
		Label: "Select the provider for the new backup account",
		Items: []string{"Google", "Microsoft"},
	}
	_, provider, err := providerPrompt.Run()
	if err != nil {
		logger.Fatal("Provider selection failed: %v", err)
	}

	// --- Step 3: Find Main Account and its Client ---
	var mainUser *model.User
	for i, user := range appCfg.Users {
		if user.Provider == provider && user.IsMain {
			mainUser = &appCfg.Users[i]
			break
		}
	}
	if mainUser == nil {
		logger.Fatal("No main account found for %s. Please run 'init' to add a main account for this provider first.", provider)
	}

	logger.Info("Found main account: %s. Proceeding to add a backup account.", mainUser.Email)
	mainClient, err := getClientForUser(ctx, mainUser, appCfg)
	if err != nil {
		logger.Fatal("Could not create API client for main account %s: %v", mainUser.Email, err)
	}

	// --- Step 4: OAuth Flow for New Backup Account ---
	var oauthCfg *oauth2.Config
	if provider == "Google" {
		oauthCfg = auth.GetGoogleOAuthConfig(appCfg)
	} else {
		oauthCfg = auth.GetMicrosoftOAuthConfig(appCfg)
	}

	token, err := auth.GetTokenFromWeb(ctx, oauthCfg)
	if err != nil {
		logger.Fatal("Authentication flow for backup account failed: %v", err)
	}
	if token.RefreshToken == "" {
		logger.Fatal("Authentication successful, but a refresh token was not provided. Cannot proceed.")
	}
	logger.Info("Successfully received authentication tokens for the new backup account.")

	// --- Step 5: Get New User's Info ---
	var backupClient api.CloudClient
	var backupUserEmail string
	if provider == "Google" {
		httpClient := oauth2.NewClient(ctx, oauth2.StaticTokenSource(token))
		backupClient, err = google.NewClient(httpClient)
	} else {
		ts := oauthCfg.TokenSource(ctx, token)
		backupClient, err = microsoft.NewClient(ts)
	}
	if err != nil {
		logger.Fatal("Failed to create API client to verify backup user: %v", err)
	}

	backupUserEmail, err = backupClient.GetUserInfo(ctx)
	if err != nil {
		logger.Fatal("Failed to retrieve backup user email from provider: %v", err)
	}
	if backupUserEmail == mainUser.Email {
		logger.Fatal("The new account's email (%s) is the same as the main account's email. Cannot add an account as a backup of itself.", backupUserEmail)
	}

	// --- Step 6: Add User to Config ---
	newUser := model.User{
		Provider:     provider,
		Email:        backupUserEmail,
		IsMain:       false,
		RefreshToken: token.RefreshToken,
	}
	appCfg.Users = append(appCfg.Users, newUser)

	logger.Info("Saving new backup account information for %s...", backupUserEmail)
	if err := config.SaveConfig(masterPassword, appCfg); err != nil {
		logger.Fatal("Failed to save configuration: %v", err)
	}

	// --- Step 7: Share Sync Folder from Main to Backup ---
	logger.Info("Sharing the sync folder from %s to %s...", mainUser.Email, backupUserEmail)

	if safeRun {
		logger.TaggedInfo("DRY RUN", "Would share the 'synched-cloud-drives' folder from %s with %s", mainUser.Email, backupUserEmail)
	} else {
		syncFolderID, err := mainClient.PreflightCheck(ctx)
		if err != nil {
			logger.Fatal("Could not find the sync folder in main account %s to share it: %v", mainUser.Email, err)
		}

		err = mainClient.ShareFolder(ctx, syncFolderID, backupUserEmail)
		if err != nil {
			logger.Fatal("Failed to share sync folder from %s to %s: %v", mainUser.Email, backupUserEmail, err)
		}
	}

	logger.Info("\nSuccessfully added and configured backup account %s.", backupUserEmail)
}

// getClientForUser is a helper function to create an API client for a specific user from config.
func getClientForUser(ctx context.Context, user *model.User, appCfg *config.AppConfig) (api.CloudClient, error) {
	ts, err := auth.NewTokenSource(ctx, user, appCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create token source for %s: %w", user.Email, err)
	}

	var client api.CloudClient
	if user.Provider == "Google" {
		httpClient := oauth2.NewClient(ctx, ts)
		client, err = google.NewClient(httpClient)
	} else if user.Provider == "Microsoft" {
		client, err = microsoft.NewClient(ts)
	} else {
		return nil, errors.New("unknown provider")
	}
	return client, err
}
