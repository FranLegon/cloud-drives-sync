package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/FranLegon/cloud-drives-sync/internal/auth"
	"github.com/FranLegon/cloud-drives-sync/internal/config"
	"github.com/FranLegon/cloud-drives-sync/internal/database"
	"github.com/FranLegon/cloud-drives-sync/internal/google"
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/FranLegon/cloud-drives-sync/internal/model"
	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
)

var (
	jsonFlag    string
	getJsonFlag bool
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize the application or add a main account",
	Long: `Initialize the application for first-time use by setting up encryption,
creating configuration files, and initializing the database. Can also be used
to add main accounts for Google Drive and Microsoft OneDrive.`,
	RunE: runInit,
}

func init() {
	initCmd.Flags().StringVarP(&jsonFlag, "json", "j", "", "JSON string containing client credentials")
	initCmd.Flags().BoolVarP(&getJsonFlag, "getjson", "g", false, "Output configuration as JSON string")
	rootCmd.AddCommand(initCmd)
}

func runInit(cmd *cobra.Command, args []string) error {
	// Check if this is first-time initialization
	if !config.ConfigExists() {
		logger.Info("First-time initialization")
		return firstTimeInit()
	}

	// If getJsonFlag is set, just load and print config
	if getJsonFlag {
		return printConfigAsJson()
	}

	// If jsonFlag is set, update credentials
	if jsonFlag != "" {
		return updateCredentialsFromJson()
	}

	// Existing installation - interactive menu
	return interactiveUpdate()
}

func firstTimeInit() error {
	var password string
	var err error

	if passwordFlag != "" {
		password = passwordFlag
		logger.Info("Using provided master password")
	} else {
		// Prompt for master password
		prompt := promptui.Prompt{
			Label: "Create Master Password",
			Mask:  '*',
		}
		password, err = prompt.Run()
		if err != nil {
			return fmt.Errorf("failed to read password: %w", err)
		}

		confirmPrompt := promptui.Prompt{
			Label: "Confirm Master Password",
			Mask:  '*',
		}
		confirmPassword, err := confirmPrompt.Run()
		if err != nil {
			return fmt.Errorf("failed to read password confirmation: %w", err)
		}

		if password != confirmPassword {
			return fmt.Errorf("passwords do not match")
		}
	}

	// Prompt for client credentials
	var cfg *model.Config

	if jsonFlag != "" {
		logger.Info("Using provided JSON configuration")
		cfg = &model.Config{}
		if err := json.Unmarshal([]byte(jsonFlag), cfg); err != nil {
			return fmt.Errorf("failed to parse JSON configuration: %w", err)
		}
		// Ensure users slice is initialized
		if cfg.Users == nil {
			cfg.Users = []model.User{}
		}
	} else {
		logger.Info("Enter API client credentials (leave blank to skip provider)")

		googleIDPrompt := promptui.Prompt{Label: "Google Client ID"}
		googleID, _ := googleIDPrompt.Run()

		googleSecretPrompt := promptui.Prompt{Label: "Google Client Secret", Mask: '*'}
		googleSecret, _ := googleSecretPrompt.Run()

		msIDPrompt := promptui.Prompt{Label: "Microsoft Client ID"}
		msID, _ := msIDPrompt.Run()

		msSecretPrompt := promptui.Prompt{Label: "Microsoft Client Secret", Mask: '*'}
		msSecret, _ := msSecretPrompt.Run()

		telegramIDPrompt := promptui.Prompt{Label: "Telegram API ID"}
		telegramID, _ := telegramIDPrompt.Run()

		telegramHashPrompt := promptui.Prompt{Label: "Telegram API Hash", Mask: '*'}
		telegramHash, _ := telegramHashPrompt.Run()

		telegramPhonePrompt := promptui.Prompt{Label: "Telegram Phone"}
		telegramPhone, _ := telegramPhonePrompt.Run()

		// Create configuration
		cfg = &model.Config{
			GoogleClient: model.GoogleClient{
				ID:     googleID,
				Secret: googleSecret,
			},
			MicrosoftClient: model.MicrosoftClient{
				ID:     msID,
				Secret: msSecret,
			},
			TelegramClient: model.TelegramClient{
				APIID:   telegramID,
				APIHash: telegramHash,
				Phone:   telegramPhone,
			},
			Users: []model.User{},
		}
	}

	// Output JSON if requested
	if getJsonFlag {
		jsonData, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal configuration: %w", err)
		}
		fmt.Println(string(jsonData))
	}

	// Save configuration
	if err := config.SaveConfig(cfg, password); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}
	logger.Info("Configuration saved successfully")

	// Create database
	if err := database.CreateDB(password); err != nil {
		return fmt.Errorf("failed to create database: %w", err)
	}
	logger.Info("Database created successfully")

	// Initialize database schema
	db, err := database.Open(password)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer db.Close()

	if err := db.Initialize(); err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	logger.Info("Database schema initialized")

	logger.Info("Initialization complete! You can now add accounts using the 'init' command")
	return nil
}

func interactiveUpdate() error {
	password, err := getPassword()
	if err != nil {
		return err
	}

	// Load configuration to check current state
	cfg, err := config.LoadConfig(password)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	prompt := promptui.Select{
		Label: "Configuration already exists. What would you like to do?",
		Items: []string{"Update Client Credentials", "Update Main Account", "Cancel"},
	}

	_, result, err := prompt.Run()
	if err != nil {
		return fmt.Errorf("prompt failed: %w", err)
	}

	switch result {
	case "Update Client Credentials":
		return updateClientCredentialsInteractive(cfg, password)
	case "Update Main Account":
		return updateMainAccount(cfg, password)
	case "Cancel":
		return nil
	}

	return nil
}

func updateClientCredentialsInteractive(cfg *model.Config, password string) error {
	logger.Info("Enter new API client credentials (leave blank to keep existing)")

	// Google
	googleIDPrompt := promptui.Prompt{Label: fmt.Sprintf("Google Client ID [%s]", maskString(cfg.GoogleClient.ID)), AllowEdit: true}
	googleID, _ := googleIDPrompt.Run()
	if googleID != "" {
		cfg.GoogleClient.ID = googleID
	}

	googleSecretPrompt := promptui.Prompt{Label: "Google Client Secret (leave blank to keep existing)", Mask: '*'}
	googleSecret, _ := googleSecretPrompt.Run()
	if googleSecret != "" {
		cfg.GoogleClient.Secret = googleSecret
	}

	// Microsoft
	msIDPrompt := promptui.Prompt{Label: fmt.Sprintf("Microsoft Client ID [%s]", maskString(cfg.MicrosoftClient.ID)), AllowEdit: true}
	msID, _ := msIDPrompt.Run()
	if msID != "" {
		cfg.MicrosoftClient.ID = msID
	}

	msSecretPrompt := promptui.Prompt{Label: "Microsoft Client Secret (leave blank to keep existing)", Mask: '*'}
	msSecret, _ := msSecretPrompt.Run()
	if msSecret != "" {
		cfg.MicrosoftClient.Secret = msSecret
	}

	// Telegram
	telegramIDPrompt := promptui.Prompt{Label: fmt.Sprintf("Telegram API ID [%s]", maskString(cfg.TelegramClient.APIID)), AllowEdit: true}
	telegramID, _ := telegramIDPrompt.Run()
	if telegramID != "" {
		cfg.TelegramClient.APIID = telegramID
	}

	telegramHashPrompt := promptui.Prompt{Label: "Telegram API Hash (leave blank to keep existing)", Mask: '*'}
	telegramHash, _ := telegramHashPrompt.Run()
	if telegramHash != "" {
		cfg.TelegramClient.APIHash = telegramHash
	}

	telegramPhonePrompt := promptui.Prompt{Label: fmt.Sprintf("Telegram Phone [%s]", cfg.TelegramClient.Phone), AllowEdit: true}
	telegramPhone, _ := telegramPhonePrompt.Run()
	if telegramPhone != "" {
		cfg.TelegramClient.Phone = telegramPhone
	}

	if err := config.SaveConfig(cfg, password); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}
	logger.Info("Client credentials updated successfully")
	return nil
}

func updateCredentialsFromJson() error {
	password, err := getPassword()
	if err != nil {
		return err
	}

	cfg, err := config.LoadConfig(password)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	var newCfg model.Config
	if err := json.Unmarshal([]byte(jsonFlag), &newCfg); err != nil {
		return fmt.Errorf("failed to parse JSON configuration: %w", err)
	}

	// Update credentials
	if newCfg.GoogleClient.ID != "" { cfg.GoogleClient.ID = newCfg.GoogleClient.ID }
	if newCfg.GoogleClient.Secret != "" { cfg.GoogleClient.Secret = newCfg.GoogleClient.Secret }
	if newCfg.MicrosoftClient.ID != "" { cfg.MicrosoftClient.ID = newCfg.MicrosoftClient.ID }
	if newCfg.MicrosoftClient.Secret != "" { cfg.MicrosoftClient.Secret = newCfg.MicrosoftClient.Secret }
	if newCfg.TelegramClient.APIID != "" { cfg.TelegramClient.APIID = newCfg.TelegramClient.APIID }
	if newCfg.TelegramClient.APIHash != "" { cfg.TelegramClient.APIHash = newCfg.TelegramClient.APIHash }
	if newCfg.TelegramClient.Phone != "" { cfg.TelegramClient.Phone = newCfg.TelegramClient.Phone }

	if err := config.SaveConfig(cfg, password); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}
	logger.Info("Client credentials updated from JSON successfully")
	return nil
}

func updateMainAccount(cfg *model.Config, password string) error {
	// Check if main account already exists
	var mainUser *model.User
	var mainIndex int
	mainExists := false
	for i, user := range cfg.Users {
		if user.IsMain {
			mainUser = &cfg.Users[i]
			mainIndex = i
			mainExists = true
			break
		}
	}

	if mainExists {
		prompt := promptui.Prompt{
			Label:     fmt.Sprintf("Main account %s already exists. Replace it? (y/N)", mainUser.Email),
			IsConfirm: true,
		}
		_, err := prompt.Run()
		if err != nil {
			return nil // User cancelled
		}
	}

	// Only Google can be the main account
	provider := "Google"
	fmt.Println("Adding Google as the main account provider.")

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
		if err != nil {
			return fmt.Errorf("failed to get user email: %w", err)
		}
	case "Microsoft":
		email, err = auth.GetMicrosoftUserEmail(ctx, token, oauthConfig)
		if err != nil {
			return fmt.Errorf("failed to get user email: %w", err)
		}
	}

	logger.Info("Authorized as: %s", email)

	// If mainExists, remove old main account from slice
	if mainExists {
		// Remove the old main account
		cfg.Users = append(cfg.Users[:mainIndex], cfg.Users[mainIndex+1:]...)
	}

	// Add user to config
	user := model.User{
		Provider:     model.Provider(provider),
		Email:        email,
		IsMain:       true,
		RefreshToken: token.RefreshToken,
	}
	config.AddUser(cfg, user)

	// Save updated configuration
	if err := config.SaveConfig(cfg, password); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	logger.Info("Main account added/updated successfully")

	// For Google, create the sync folder
	if provider == "Google" {
		client, err := google.NewClient(&user, oauthConfig)
		if err != nil {
			return fmt.Errorf("failed to create client: %w", err)
		}

		// Check if sync folder exists
		if err := client.PreFlightCheck(); err != nil {
			// Folder doesn't exist, create it
			logger.Info("Creating sync folder...")
			if _, err := client.CreateSyncFolder(); err != nil {
				return fmt.Errorf("failed to create sync folder: %w", err)
			}
		}
	}

	return nil
}

func getPassword() (string, error) {
	if passwordFlag != "" {
		return passwordFlag, nil
	}
	prompt := promptui.Prompt{
		Label: "Master Password",
		Mask:  '*',
	}
	return prompt.Run()
}

func maskString(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return s[:2] + "****" + s[len(s)-2:]
}

func printConfigAsJson() error {
	var password string
	var err error

	if passwordFlag != "" {
		password = passwordFlag
	} else {
		// Prompt for password
		prompt := promptui.Prompt{
			Label: "Master Password",
			Mask:  '*',
		}
		password, err = prompt.Run()
		if err != nil {
			return fmt.Errorf("failed to read password: %w", err)
		}
	}

	cfg, err := config.LoadConfig(password)
	if err != nil {
		return fmt.Errorf("failed to load configuration: %w", err)
	}

	jsonData, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal configuration: %w", err)
	}
	fmt.Println(string(jsonData))
	return nil
}
