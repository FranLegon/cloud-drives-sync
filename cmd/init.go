package cmd

import (
	"cloud-drives-sync/internal/auth"
	"cloud-drives-sync/internal/config"
	"cloud-drives-sync/internal/database"
	"cloud-drives-sync/internal/logger"
	"cloud-drives-sync/internal/model"
	"fmt"
	"os"

	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize the application or add a new main account.",
	Long: `Performs first-time setup by creating encrypted configuration and database files.
It prompts for a master password and cloud provider API credentials.
Run this command again to add a new main account for a provider.`,
	Run: runInit,
}

func runInit(cmd *cobra.Command, args []string) {
	var cfg *config.Config
	var password string
	configPath, _ := config.GetConfigPath(config.ConfigFile)

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		logger.Info("First-time setup detected. Welcome!")
		password = promptForNewPassword()
		cfg = createNewConfig()
	} else {
		password = promptForPassword("Enter Master Password to add new main account")
		loadedCfg, err := config.LoadConfig(password)
		if err != nil {
			logger.Error(err, "Failed to decrypt existing configuration")
		}
		cfg = loadedCfg
		logger.Info("Configuration loaded. Proceeding to add a new main account.")
	}

	addMainAccount(cfg, password)

	if err := cfg.SaveConfig(password); err != nil {
		logger.Error(err, "Failed to save configuration")
	}

	db, err := database.Connect(password)
	if err != nil {
		logger.Error(err, "Failed to create or connect to the encrypted database")
	}
	defer db.Close()

	logger.Info("Initialization/update complete. You can now add backup accounts using the 'add-account' command.")
}

func promptForNewPassword() string {
	validate := func(input string) error {
		if len(input) < 8 {
			return fmt.Errorf("password must be at least 8 characters long")
		}
		return nil
	}
	prompt := promptui.Prompt{
		Label:    "Create Master Password",
		Validate: validate,
		Mask:     '*',
	}
	password, err := prompt.Run()
	if err != nil {
		logger.Error(err, "Prompt failed")
	}

	prompt = promptui.Prompt{
		Label: "Confirm Master Password",
		Mask:  '*',
	}
	confirm, err := prompt.Run()
	if err != nil {
		logger.Error(err, "Prompt failed")
	}

	if password != confirm {
		logger.Error(fmt.Errorf("passwords do not match"), "Password confirmation failed")
	}
	return password
}

func createNewConfig() *config.Config {
	cfg := &config.Config{}
	logger.Info("Please provide your OAuth Client credentials.")
	logger.Info("For details, see the project's README on setting up API access.")

	logger.Info("\nEnter Google Client credentials (from Google Cloud Console):")
	cfg.GoogleClient.ID = promptForInput("Google Client ID")
	cfg.GoogleClient.Secret = promptForInput("Google Client Secret")

	logger.Info("\nEnter Microsoft Client credentials (from Azure Portal):")
	cfg.MicrosoftClient.ID = promptForInput("Microsoft Client ID")
	cfg.MicrosoftClient.Secret = promptForInput("Microsoft Client Secret")

	return cfg
}

func addMainAccount(cfg *config.Config, password string) {
	prompt := promptui.Select{
		Label: "Select provider for the new Main Account",
		Items: []string{"Google", "Microsoft"},
	}
	_, provider, err := prompt.Run()
	if err != nil {
		logger.Error(err, "Provider selection failed")
	}

	logger.Info("Proceeding with OAuth flow for %s...", provider)
	refreshToken, err := auth.StartOAuthFlow(provider, cfg)
	if err != nil {
		logger.Error(err, "OAuth flow failed")
	}

	email, err := getEmailFromToken(provider, cfg, refreshToken)
	if err != nil {
		logger.Error(err, "Could not verify new account's email")
	}

	for _, u := range cfg.Users {
		if u.Email == email {
			logger.Error(nil, "Account %s is already configured. Cannot add duplicate.", email)
		}
	}

	newUser := model.User{
		Provider:     provider,
		Email:        email,
		IsMain:       true,
		RefreshToken: refreshToken,
	}
	cfg.Users = append(cfg.Users, newUser)

	logger.Info("Successfully authorized %s. Now creating the 'synched-cloud-drives' folder...", email)

	tempClient, err := createClient(provider, cfg, newUser)
	if err != nil {
		logger.Error(err, "Failed to create temporary client")
	}

	folderID, err := tempClient.PreFlightCheck()
	if err != nil {
		logger.Error(err, "Pre-flight check failed for new account")
	}

	if folderID == "" {
		if _, err := tempClient.CreateRootSyncFolder(); err != nil {
			logger.Error(err, "Failed to create 'synched-cloud-drives' folder")
		}
		logger.Info("Successfully created 'synched-cloud-drives' folder in %s.", email)
	} else {
		logger.Info("'synched-cloud-drives' folder already exists.")
	}
}

func promptForInput(label string) string {
	prompt := promptui.Prompt{Label: label}
	result, err := prompt.Run()
	if err != nil {
		logger.Error(err, "Prompt failed")
	}
	return result
}
