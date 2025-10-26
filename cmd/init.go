package cmd

import (
	"context"
	"fmt"
	"os"

	"cloud-drives-sync/internal/auth"
	"cloud-drives-sync/internal/config"
	"cloud-drives-sync/internal/crypto"
	"cloud-drives-sync/internal/database"
	"cloud-drives-sync/internal/google"
	"cloud-drives-sync/internal/logger"
	"cloud-drives-sync/internal/microsoft"
	"cloud-drives-sync/internal/model"

	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize the application or add a main account",
	Long: `Initialize the application on first run by creating encrypted config
and database files. Can also be used to add main accounts for providers.`,
	RunE: runInit,
}

func runInit(cmd *cobra.Command, args []string) error {
	ctx := getContext()
	log := logger.New().WithPrefix("INIT")

	// Check if this is first-time setup
	firstTime := !fileExists(config.ConfigFileName) || !fileExists(crypto.SaltFileName)

	var password string
	var cfg *config.Config

	if firstTime {
		log.Info("First-time setup detected")

		// Generate salt
		log.Info("Generating encryption salt...")
		salt, err := crypto.GenerateSalt()
		if err != nil {
			log.Fatal("Failed to generate salt: %v", err)
		}

		if err := crypto.SaveSalt(salt, crypto.SaltFileName); err != nil {
			log.Fatal("Failed to save salt: %v", err)
		}
		log.Success("Salt generated and saved")

		// Get master password
		fmt.Print("Create a master password: ")
		fmt.Scanln(&password)

		fmt.Print("Confirm master password: ")
		var confirm string
		fmt.Scanln(&confirm)

		if password != confirm {
			log.Fatal("Passwords do not match")
		}

		// Get client credentials
		fmt.Print("Enter Google OAuth Client ID (or press Enter to skip): ")
		var googleID string
		fmt.Scanln(&googleID)

		fmt.Print("Enter Google OAuth Client Secret: ")
		var googleSecret string
		fmt.Scanln(&googleSecret)

		fmt.Print("Enter Microsoft OAuth Client ID (or press Enter to skip): ")
		var msID string
		fmt.Scanln(&msID)

		fmt.Print("Enter Microsoft OAuth Client Secret: ")
		var msSecret string
		fmt.Scanln(&msSecret)

		// Create config
		cfg = &config.Config{
			GoogleClient: config.ClientCredentials{
				ID:     googleID,
				Secret: googleSecret,
			},
			MicrosoftClient: config.ClientCredentials{
				ID:     msID,
				Secret: msSecret,
			},
			Users: []model.User{},
		}

		// Save config
		if err := config.Save(cfg, password); err != nil {
			log.Fatal("Failed to save config: %v", err)
		}
		log.Success("Configuration saved")

		// Initialize database
		log.Info("Initializing database...")
		if err := database.SetupDatabase(password); err != nil {
			log.Fatal("Failed to initialize database: %v", err)
		}
		log.Success("Database initialized")

	} else {
		// Load existing config
		var err error
		cfg, password, err = loadConfig()
		if err != nil {
			log.Fatal("Failed to load config: %v", err)
		}
		log.Info("Configuration loaded")
	}

	// Prompt to add a main account
	fmt.Print("Add a main account? (google/microsoft/no): ")
	var choice string
	fmt.Scanln(&choice)

	if choice == "google" || choice == "g" {
		if err := addMainAccount(ctx, cfg, password, model.ProviderGoogle); err != nil {
			log.Fatal("Failed to add Google account: %v", err)
		}
	} else if choice == "microsoft" || choice == "m" || choice == "ms" {
		if err := addMainAccount(ctx, cfg, password, model.ProviderMicrosoft); err != nil {
			log.Fatal("Failed to add Microsoft account: %v", err)
		}
	}

	log.Success("Initialization complete")
	return nil
}

func addMainAccount(ctx context.Context, cfg *config.Config, password string, provider model.Provider) error {
	log := logger.New().WithPrefix(string(provider))

	// Check if main account already exists
	if cfg.HasMainAccount(provider) {
		log.Fatal("Main account already exists for %s", provider)
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
		return err
	}

	// Perform OAuth flow
	log.Info("Starting OAuth flow...")
	refreshToken, err := auth.PerformOAuthFlow(ctx, oauthConfig, log)
	if err != nil {
		return fmt.Errorf("OAuth flow failed: %w", err)
	}

	// Create token source and client
	tokenSource := auth.TokenSource(ctx, oauthConfig, refreshToken)

	var client interface{}
	var email string

	if provider == model.ProviderGoogle {
		client, err = google.NewClient(ctx, tokenSource)
		if err != nil {
			return err
		}
		googleClient := client.(interface {
			GetUserEmail(context.Context) (string, error)
		})
		email, _ = googleClient.GetUserEmail(ctx)
	} else {
		client, err = microsoft.NewClient(ctx, tokenSource)
		if err != nil {
			return err
		}
		msClient := client.(interface {
			GetUserEmail(context.Context) (string, error)
		})
		email, _ = msClient.GetUserEmail(ctx)
	}

	log.Info("Authenticated as: %s", email)

	// Add user to config
	user := model.User{
		Provider:     provider,
		Email:        email,
		IsMain:       true,
		RefreshToken: refreshToken,
	}
	cfg.AddUser(user)

	// Save config
	if err := config.Save(cfg, password); err != nil {
		return fmt.Errorf("failed to save config: %w", err)
	}

	// Create sync folder
	log.Info("Creating sync folder...")
	if apiClient, ok := client.(interface {
		GetOrCreateFolder(context.Context, string, string) (string, error)
	}); ok {
		folderID, err := apiClient.GetOrCreateFolder(ctx, "synched-cloud-drives", "")
		if err != nil {
			return fmt.Errorf("failed to create sync folder: %w", err)
		}
		log.Success("Sync folder created (ID: %s)", folderID)
	}

	log.Success("Main account added: %s", email)
	return nil
}

func fileExists(filename string) bool {
	_, err := os.Stat(filename)
	return err == nil
}
