package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/FranLegon/cloud-drives-sync/internal/auth"
	"github.com/FranLegon/cloud-drives-sync/internal/config"
	"github.com/FranLegon/cloud-drives-sync/internal/google"
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/FranLegon/cloud-drives-sync/internal/model"
	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
)

var addAccountCmd = &cobra.Command{
	Use:   "add-account",
	Short: "Add a backup account for an existing provider",
	Long: `Adds a backup account to a provider that already has a configured main account.
Google Drive: Shares the main account's sync folder with the backup account.
Microsoft OneDrive: Creates a new sync folder in the backup account and shares with main.
Telegram: Adds the single Telegram account for backup storage.`,
	RunE: runAddAccount,
}

func init() {
	rootCmd.AddCommand(addAccountCmd)
}

func runAddAccount(cmd *cobra.Command, args []string) error {
	// Prompt for provider
	providerPrompt := promptui.Select{
		Label: "Select Provider",
		Items: []string{"Google", "Microsoft", "Telegram"},
	}
	_, provider, err := providerPrompt.Run()
	if err != nil {
		return fmt.Errorf("failed to select provider: %w", err)
	}

	// Check if main account exists (except for Telegram)
	if provider != "Telegram" {
		mainAccount := config.GetMainAccount(cfg, model.Provider(provider))
		if mainAccount == nil {
			return fmt.Errorf("no main account found for %s - please add one using 'init' first", provider)
		}
	}

	// Perform OAuth flow
	ctx := context.Background()
	var oauthConfig *oauth2.Config
	var email string

	switch provider {
	case "Google":
		oauthConfig = auth.GetGoogleOAuthConfig(cfg.GoogleClient.ID, cfg.GoogleClient.Secret)
	case "Microsoft":
		oauthConfig = auth.GetMicrosoftOAuthConfig(cfg.MicrosoftClient.ID, cfg.MicrosoftClient.Secret)
	case "Telegram":
		// TODO: Implement Telegram authentication
		return fmt.Errorf("Telegram account addition not yet implemented")
	default:
		return fmt.Errorf("unsupported provider: %s", provider)
	}

	// Start OAuth server
	server := auth.NewOAuthServer()
	if err := server.Start(); err != nil {
		return fmt.Errorf("failed to start OAuth server: %w", err)
	}
	defer server.Stop()

	// Generate state and auth URL
	state := auth.GenerateStateToken()
	authURL := oauthConfig.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"))

	logger.Info("Please visit the following URL to authorize:")
	fmt.Println(authURL)

	// Wait for callback
	code, err := server.WaitForCode(state, 120*time.Second)
	if err != nil {
		return fmt.Errorf("authorization failed: %w", err)
	}

	// Exchange code for token
	token, err := auth.ExchangeCode(ctx, oauthConfig, code)
	if err != nil {
		return fmt.Errorf("failed to exchange code: %w", err)
	}

	// Get user email
	switch provider {
	case "Google":
		email, err = auth.GetGoogleUserEmail(ctx, token, oauthConfig)
	case "Microsoft":
		email, err = auth.GetMicrosoftUserEmail(ctx, token, oauthConfig)
	}
	if err != nil {
		return fmt.Errorf("failed to get user email: %w", err)
	}

	logger.Info("Authorized as: %s", email)
	if token.RefreshToken != "" {
		logger.Info("Refresh token received successfully")
	} else {
		logger.Warning("No refresh token received - this may cause issues later")
	}

	// Create user record
	user := model.User{
		Provider:     model.Provider(provider),
		Email:        email,
		IsMain:       false,
		RefreshToken: token.RefreshToken,
	}

	// Provider-specific setup
	switch provider {
	case "Google":
		// Share main account's folder with backup account
		mainUser := config.GetMainAccount(cfg, model.ProviderGoogle)
		mainClient, err := google.NewClient(mainUser, oauthConfig)
		if err != nil {
			return fmt.Errorf("failed to create main client: %w", err)
		}

		syncFolderID, err := mainClient.GetSyncFolderID()
		if err != nil {
			return fmt.Errorf("failed to get sync folder: %w", err)
		}

		if !safeMode {
			if err := mainClient.ShareFolder(syncFolderID, email, "writer"); err != nil {
				return fmt.Errorf("failed to share folder: %w", err)
			}
			logger.Info("Shared main sync folder with backup account")
		} else {
			logger.DryRun("Would share main sync folder with %s", email)
		}

	case "Microsoft":
		// Prompt for sync folder name
		folderPrompt := promptui.Prompt{
			Label:   "Sync Folder Name (e.g., synched-cloud-drives-1)",
			Default: "synched-cloud-drives-1",
		}
		syncFolderName, err := folderPrompt.Run()
		if err != nil {
			return fmt.Errorf("failed to get folder name: %w", err)
		}
		user.SyncFolderName = syncFolderName

		// TODO: Create folder in backup account and share with main
		logger.Info("Microsoft OneDrive folder setup not yet implemented")
	}

	// Add user to config
	config.AddUser(cfg, user)

	// Save updated configuration
	if err := config.SaveConfig(cfg, masterPassword); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	logger.Info("Backup account added successfully")
	return nil
}
