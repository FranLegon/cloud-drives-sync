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

	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
)

// initCmd represents the init command
var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize the application or add a new main account",
	Long: `Handles the first-time setup for cloud-drives-sync. This includes:
- Creating a master password for encryption.
- Generating a cryptographic salt.
- Creating the encrypted configuration and database files.
- Guiding you through the OAuth 2.0 process to add your first main account for either Google or Microsoft.

If run after the initial setup, this command can be used to add a main account for a provider
that has not yet been configured (e.g., adding a main Microsoft account if only Google is set up).`,
	Run: runInit,
}

func runInit(cmd *cobra.Command, args []string) {
	ctx := context.Background()
	var appCfg *config.AppConfig
	var masterPassword string
	isFirstTime := false

	// --- Step 1: Handle Salt and Config File Existence ---
	_, saltErr := os.Stat("config.salt")
	_, cfgErr := os.Stat("config.json.enc")

	if os.IsNotExist(saltErr) || os.IsNotExist(cfgErr) {
		logger.Info("First time setup: Welcome to cloud-drives-sync!")
		isFirstTime = true

		// Generate Salt
		logger.Info("Generating new cryptographic salt...")
		if _, err := crypto.GenerateSalt(); err != nil {
			logger.Fatal("Failed to generate salt file: %v", err)
		}

		// Get Master Password with confirmation
		var err error
		masterPassword, err = config.GetMasterPassword(true) // true for confirmation
		if err != nil {
			logger.Fatal("Failed to get master password: %v", err)
		}
		appCfg = &config.AppConfig{Users: []model.User{}}
	} else {
		logger.Info("Configuration files found. Running in 'add main account' mode.")
		var err error
		masterPassword, err = config.GetMasterPassword(false) // false for no confirmation
		if err != nil {
			logger.Fatal("Failed to get master password: %v", err)
		}
		appCfg, err = config.LoadConfig(masterPassword)
		if err != nil {
			logger.Fatal("Failed to load config: %v", err)
		}
	}

	// --- Step 2: Select Provider ---
	providerPrompt := promptui.Select{
		Label: "Select a provider for the new main account",
		Items: []string{"Google", "Microsoft"},
	}
	_, provider, err := providerPrompt.Run()
	if err != nil {
		logger.Fatal("Provider selection failed: %v", err)
	}

	// Check if a main account for this provider already exists
	for _, user := range appCfg.Users {
		if user.Provider == provider && user.IsMain {
			logger.Fatal("A main account for %s already exists. Use 'add-account' to add a backup account.", provider)
		}
	}

	// --- Step 3: Get Client Credentials ---
	if (provider == "Google" && appCfg.GoogleClient.ID == "") || (provider == "Microsoft" && appCfg.MicrosoftClient.ID == "") {
		logger.Info("Please provide the OAuth 2.0 Client Credentials for %s.", provider)
		logger.Info("You can get these from Google Cloud Platform or Azure Portal.")

		idPrompt := promptui.Prompt{Label: fmt.Sprintf("%s Client ID", provider)}
		clientID, err := idPrompt.Run()
		if err != nil {
			logger.Fatal("Could not read Client ID: %v", err)
		}

		secretPrompt := promptui.Prompt{Label: fmt.Sprintf("%s Client Secret", provider), Mask: '*'}
		clientSecret, err := secretPrompt.Run()
		if err != nil {
			logger.Fatal("Could not read Client Secret: %v", err)
		}

		if provider == "Google" {
			appCfg.GoogleClient = config.ClientCredentials{ID: clientID, Secret: clientSecret}
		} else {
			appCfg.MicrosoftClient = config.ClientCredentials{ID: clientID, Secret: clientSecret}
		}
	}

	// --- Step 4: OAuth 2.0 Flow ---
	var oauthCfg *oauth2.Config
	if provider == "Google" {
		oauthCfg = auth.GetGoogleOAuthConfig(appCfg)
	} else {
		oauthCfg = auth.GetMicrosoftOAuthConfig(appCfg)
	}

	token, err := auth.GetTokenFromWeb(ctx, oauthCfg)
	if err != nil {
		logger.Fatal("Authentication flow failed: %v", err)
	}
	if token.RefreshToken == "" {
		logger.Fatal("Authentication successful, but a refresh token was not provided. Cannot proceed.")
	}
	logger.Info("Successfully received authentication tokens.")

	// --- Step 5: Get User Info and Save Config ---
	var apiClient api.CloudClient
	var userEmail string
	if provider == "Google" {
		httpClient := oauth2.NewClient(ctx, oauth2.StaticTokenSource(token))
		apiClient, err = google.NewClient(httpClient)
	} else {
		ts := oauthCfg.TokenSource(ctx, token)
		apiClient, err = microsoft.NewClient(ts)
	}
	if err != nil {
		logger.Fatal("Failed to create API client to verify user: %v", err)
	}

	userEmail, err = apiClient.GetUserInfo(ctx)
	if err != nil {
		logger.Fatal("Failed to retrieve user email from provider: %v", err)
	}

	newUser := model.User{
		Provider:     provider,
		Email:        userEmail,
		IsMain:       true,
		RefreshToken: token.RefreshToken,
	}
	appCfg.Users = append(appCfg.Users, newUser)

	logger.Info("Saving new account information for %s to encrypted config...", userEmail)
	if err := config.SaveConfig(masterPassword, appCfg); err != nil {
		logger.Fatal("Failed to save configuration: %v", err)
	}

	// --- Step 6: Initialize Database and Create Sync Folder ---
	logger.Info("Initializing encrypted metadata database...")
	db, err := database.NewDB(masterPassword)
	if err != nil {
		logger.Fatal("Failed to create or connect to the database: %v", err)
	}
	if err := db.InitSchema(); err != nil {
		logger.Fatal("Failed to initialize database schema: %v", err)
	}
	db.Close()

	logger.Info("Performing post-authentication setup (creating sync folder)...")
	if _, err := apiClient.PreflightCheck(ctx); err != nil {
		logger.Fatal("Failed to create the '%s' folder in account %s: %v", api.SyncFolderName, userEmail, err)
	}

	if isFirstTime {
		logger.Info("\nInitialization complete! You can now use other commands.")
	} else {
		logger.Info("\nSuccessfully added %s as the main account for %s.", userEmail, provider)
	}
}
