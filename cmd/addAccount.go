package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/FranLegon/cloud-drives-sync/internal/auth"
	"github.com/FranLegon/cloud-drives-sync/internal/config"
	"github.com/FranLegon/cloud-drives-sync/internal/google"
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/FranLegon/cloud-drives-sync/internal/microsoft"
	"github.com/FranLegon/cloud-drives-sync/internal/model"
	"github.com/FranLegon/cloud-drives-sync/internal/telegram"
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
Telegram: Adds a Telegram account for backup storage.`,
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

	// Check if main account exists (must be Google)
	mainAccount := config.GetMainAccount(cfg, model.ProviderGoogle)
	if mainAccount == nil {
		return fmt.Errorf("no main account found - please add a Google main account using 'init' first")
	}

	if provider == "Telegram" {
		return addTelegramAccount(cfg, masterPassword)
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

	// Check if this email is already registered as a main account
	if existingMain := config.GetMainAccount(cfg, model.Provider(provider)); existingMain != nil && existingMain.Email == email {
		return fmt.Errorf("account %s is already registered as a main account", email)
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
		defaultName := fmt.Sprintf("sync-cloud-drives-%s", email)
		folderPrompt := promptui.Prompt{
			Label:   "Sync Folder Name",
			Default: defaultName,
		}
		syncFolderName, err := folderPrompt.Run()
		if err != nil {
			return fmt.Errorf("failed to get folder name: %w", err)
		}
		user.SyncFolderName = syncFolderName

		// Create folder in backup account
		if !safeMode {
			client, err := microsoft.NewClient(&user, oauthConfig)
			if err != nil {
				return fmt.Errorf("failed to create microsoft client: %w", err)
			}

			logger.Info("Creating sync folder '%s'...", syncFolderName)
			if _, err := client.CreateFolder("root", syncFolderName); err != nil {
				return fmt.Errorf("failed to create sync folder: %w", err)
			}
			logger.Info("Sync folder created successfully")
		} else {
			logger.DryRun("Would create sync folder '%s' in Microsoft account", syncFolderName)
		}
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

func addTelegramAccount(cfg *model.Config, password string) error {
	// Check if Telegram credentials are configured
	if cfg.TelegramClient.APIID == "" || cfg.TelegramClient.APIHash == "" {
		return fmt.Errorf("telegram credentials not configured - please run 'init' to configure them")
	}

	// Prompt for phone number
	prompt := promptui.Prompt{
		Label: "Enter Telegram Phone Number (e.g. +1234567890)",
	}
	phone, err := prompt.Run()
	if err != nil {
		return fmt.Errorf("failed to get phone number: %w", err)
	}

	// Create user record
	user := model.User{
		Provider: model.ProviderTelegram,
		Phone:    phone,
		IsMain:   false,
	}

	// Initialize client
	client, err := telegram.NewClient(&user, cfg.TelegramClient.APIID, cfg.TelegramClient.APIHash)
	if err != nil {
		return fmt.Errorf("failed to create telegram client: %w", err)
	}
	defer client.Close()

	// Authenticate
	logger.Info("Starting Telegram authentication for %s...", user.Phone)
	if err := client.Authenticate(user.Phone); err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	logger.Info("Authentication successful!")

	// Perform pre-flight check to ensure channel exists
	if !safeMode {
		if err := client.PreFlightCheck(); err != nil {
			return fmt.Errorf("pre-flight check failed: %w", err)
		}
	} else {
		logger.DryRun("Would perform pre-flight check and create sync channel if needed")
	}

	// Add user to config
	config.AddUser(cfg, user)

	// Save updated configuration
	if err := config.SaveConfig(cfg, password); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	logger.Info("Telegram backup account added successfully")
	return nil
}
