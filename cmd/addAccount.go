package cmd

import (
	"fmt"

	"cloud-drives-sync/internal/api"
	"cloud-drives-sync/internal/auth"
	"cloud-drives-sync/internal/config"
	"cloud-drives-sync/internal/google"
	"cloud-drives-sync/internal/logger"
	"cloud-drives-sync/internal/microsoft"
	"cloud-drives-sync/internal/model"

	"github.com/spf13/cobra"
)

var addAccountCmd = &cobra.Command{
	Use:   "add-account",
	Short: "Add a backup account to an existing provider",
	Long: `Add a backup account to a provider that already has a main account configured.
The main account will share its sync folder with the backup account.`,
	RunE: runAddAccount,
}

func runAddAccount(cmd *cobra.Command, args []string) error {
	ctx := getContext()
	log := logger.New().WithPrefix("ADD-ACCOUNT")

	// Load config
	cfg, password, err := loadConfig()
	if err != nil {
		log.Fatal("Failed to load config: %v", err)
	}

	// Prompt for provider
	fmt.Print("Add backup account for (google/microsoft): ")
	var choice string
	fmt.Scanln(&choice)

	var provider model.Provider
	if choice == "google" || choice == "g" {
		provider = model.ProviderGoogle
	} else if choice == "microsoft" || choice == "m" || choice == "ms" {
		provider = model.ProviderMicrosoft
	} else {
		log.Fatal("Invalid provider choice")
	}

	// Check if main account exists
	if !cfg.HasMainAccount(provider) {
		log.Fatal("No main account configured for %s. Please run 'init' first.", provider)
	}

	// Get OAuth config
	var clientID, clientSecret string
	if provider == model.ProviderGoogle {
		clientID = cfg.GoogleClient.ID
		clientSecret = cfg.GoogleClient.Secret
	} else {
		clientID = cfg.MicrosoftClient.ID
		clientSecret = cfg.MicrosoftClient.Secret
	}

	oauthConfig, err := auth.OAuthConfig(provider, clientID, clientSecret)
	if err != nil {
		log.Fatal("Failed to create OAuth config: %v", err)
	}

	// Perform OAuth flow
	log.Info("Starting OAuth flow for backup account...")
	refreshToken, err := auth.PerformOAuthFlow(ctx, oauthConfig, log)
	if err != nil {
		log.Fatal("OAuth flow failed: %v", err)
	}

	// Create token source and client
	tokenSource := auth.TokenSource(ctx, oauthConfig, refreshToken)

	var client api.CloudClient
	if provider == model.ProviderGoogle {
		client, err = google.NewClient(ctx, tokenSource)
	} else {
		client, err = microsoft.NewClient(ctx, tokenSource)
	}
	if err != nil {
		log.Fatal("Failed to create client: %v", err)
	}

	email, err := client.GetUserEmail(ctx)
	if err != nil {
		log.Fatal("Failed to get user email: %v", err)
	}

	log.Info("Authenticated as: %s", email)

	// Add user to config
	user := model.User{
		Provider:     provider,
		Email:        email,
		IsMain:       false,
		RefreshToken: refreshToken,
	}
	cfg.AddUser(user)

	// Save config
	if err := config.Save(cfg, password); err != nil {
		log.Fatal("Failed to save config: %v", err)
	}

	// Share sync folder with backup account
	log.Info("Sharing sync folder with backup account...")
	mainAccount, _ := cfg.GetMainAccount(provider)

	// Create main account client
	mainOAuthConfig, _ := auth.OAuthConfig(provider, clientID, clientSecret)
	mainTokenSource := auth.TokenSource(ctx, mainOAuthConfig, mainAccount.RefreshToken)

	var mainClient api.CloudClient
	if provider == model.ProviderGoogle {
		mainClient, _ = google.NewClient(ctx, mainTokenSource)
	} else {
		mainClient, _ = microsoft.NewClient(ctx, mainTokenSource)
	}

	// Get or create sync folder
	folderID, err := mainClient.GetOrCreateFolder(ctx, "synched-cloud-drives", "")
	if err != nil {
		log.Warning("Failed to get or create sync folder: %v", err)
	} else {
		// Share folder with the backup account
		if err := mainClient.ShareFolder(ctx, folderID, email, "writer"); err != nil {
			log.Error("Failed to share folder: %v", err)
		} else {
			log.Success("Shared sync folder with %s", email)

			// Verify the permission was created
			hasAccess, err := mainClient.CheckFolderPermission(ctx, folderID, email)
			if err != nil {
				log.Warning("Could not verify folder permission: %v", err)
			} else if hasAccess {
				log.Info("Verified: %s has access to the sync folder", email)
			} else {
				log.Warning("Permission created but verification failed - user may need to accept invitation")
			}
		}
	}

	log.Success("Backup account added: %s", email)
	return nil
}
