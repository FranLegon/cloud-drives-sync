package cmd

import (
	"cloud-drives-sync/internal/api"
	"cloud-drives-sync/internal/auth"
	"cloud-drives-sync/internal/config"
	"cloud-drives-sync/internal/database"
	"cloud-drives-sync/internal/google"
	"cloud-drives-sync/internal/logger"
	"cloud-drives-sync/internal/microsoft"
	"cloud-drives-sync/internal/task"
	"context"
	"fmt"
	"os"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
)

var (
	safeRun bool
)

// rootCmd represents the base command when called without any subcommands.
var rootCmd = &cobra.Command{
	Use:   "cloud-drives-sync",
	Short: "A tool to manage and synchronize files across Google Drive and OneDrive.",
	Long: `cloud-drives-sync helps de-duplicate files, mirror data between providers,
and balance storage usage across your configured cloud accounts.

All configuration and metadata is stored in encrypted files (config.json.enc, metadata.db)
in the same directory as the executable, protected by a master password.`,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		// This function runs before any command. We let each command decide
		// whether to call the setup helper for configuration and client initialization.
	},
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is the main entry point for the CLI application.
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	// A persistent flag is available to all child commands.
	rootCmd.PersistentFlags().BoolVarP(&safeRun, "safe", "s", false, "Perform a dry run without making remote changes (--safe)")

	// Register all the command files.
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(addAccountCmd)
	rootCmd.AddCommand(getMetadataCmd)
	rootCmd.AddCommand(checkForDuplicatesCmd)
	rootCmd.AddCommand(removeDuplicatesCmd)
	rootCmd.AddCommand(removeDuplicatesUnsafeCmd)
	rootCmd.AddCommand(syncProvidersCmd)
	rootCmd.AddCommand(balanceStorageCmd)
	rootCmd.AddCommand(freeMainCmd)
	rootCmd.AddCommand(checkTokensCmd)
	rootCmd.AddCommand(shareWithMainCmd)
}

// promptForPassword is a shared utility for commands to get the master password.
func promptForPassword(label string) string {
	prompt := promptui.Prompt{
		Label: label,
		Mask:  '*',
	}
	result, err := prompt.Run()
	if err != nil {
		// If the user cancels the prompt (e.g., with Ctrl+C), we should exit gracefully.
		logger.Info("Operation cancelled.")
		os.Exit(0)
	}
	return result
}

// A new type that adapts oauth2.TokenSource to azcore.TokenCredential for the MS Graph SDK.
type tokenSourceAdapter struct {
	ts oauth2.TokenSource
}

func (t *tokenSourceAdapter) GetToken(ctx context.Context, opts azcore.TokenRequestOptions) (azcore.AccessToken, error) {
	token, err := t.ts.Token()
	if err != nil {
		return azcore.AccessToken{}, err
	}
	return azcore.AccessToken{
		Token:     token.AccessToken,
		ExpiresOn: token.Expiry,
	}, nil
}

// setup is a centralized helper that all operational commands call. It handles:
// 1. Prompting for the master password.
// 2. Loading and decrypting the configuration.
// 3. Connecting to the encrypted database.
// 4. Initializing API clients for every configured user.
// 5. Assembling the TaskRunner with all necessary components.
func setup() *task.TaskRunner {
	password := promptForPassword("Enter Master Password")

	cfg, err := config.LoadConfig(password)
	if err != nil {
		logger.Error(err, "Failed to load configuration")
	}

	db, err := database.Connect(password)
	if err != nil {
		logger.Error(err, "Failed to connect to database")
	}

	clients := make(map[string]api.CloudClient)
	ctx := context.Background()

	for _, user := range cfg.Users {
		var client api.CloudClient
		var err error

		switch user.Provider {
		case "Google":
			ts, tokenErr := auth.GetGoogleTokenSource(ctx, cfg, user.RefreshToken)
			if tokenErr != nil {
				logger.Warn(user.Email, tokenErr, "could not create google token source")
				continue
			}
			client, err = google.NewClient(ctx, ts, user.Email)

		case "Microsoft":
			ts, tokenErr := auth.GetMicrosoftTokenSource(ctx, cfg, user.RefreshToken)
			if tokenErr != nil {
				logger.Warn(user.Email, tokenErr, "could not create microsoft token source")
				continue
			}
			adapter := &tokenSourceAdapter{ts: ts}
			client, err = microsoft.NewClient(ctx, adapter, user.Email)
		}

		if err != nil {
			logger.Warn(user.Email, err, "failed to create API client")
			continue
		}
		clients[user.Email] = client
	}

	return &task.TaskRunner{
		Config:    cfg,
		DB:        db,
		Clients:   clients,
		IsSafeRun: safeRun,
	}
}```

---

### **cmd/init.go**
```go
package cmd

import (
	"cloud-drives-sync/internal/auth"
	"cloud-drives-sync/internal/config"
	"cloud-drives-sync/internal/database"
	"cloud-drives-sync/internal/google"
	"cloud-drives-sync/internal/logger"
	"cloud-drives-sync/internal/microsoft"
	"cloud-drives-sync/internal/model"
	"cloud-drives-sync/internal/api"
	"context"
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

	// Create/connect to the database to ensure it's set up.
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
	
	// Check if this user already exists
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

	// Create a temporary client to set up the root folder.
	tempClient, err := createTempClient(provider, cfg, newUser)
	if err != nil { logger.Error(err, "Failed to create temporary client") }

	folderID, err := tempClient.PreFlightCheck()
	if err != nil { logger.Error(err, "Pre-flight check failed for new account") }

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

// getEmailFromToken creates a temporary client just to get the user's email for configuration.
func getEmailFromToken(provider string, cfg *config.Config, refreshToken string) (string, error) {
	tempUser := model.User{Email: "temp", RefreshToken: refreshToken, Provider: provider}
	client, err := createTempClient(provider, cfg, tempUser)
	if err != nil { return "", err }
	return client.GetUserEmail()
}

// createTempClient is a helper for creating a single-use client.
func createTempClient(provider string, cfg *config.Config, user model.User) (api.CloudClient, error) {
	ctx := context.Background()
	switch provider {
	case "Google":
		ts, err := auth.GetGoogleTokenSource(ctx, cfg, user.RefreshToken)
		if err != nil { return nil, err }
		return google.NewClient(ctx, ts, user.Email)
	case "Microsoft":
		ts, err := auth.GetMicrosoftTokenSource(ctx, cfg, user.RefreshToken)
		if err != nil { return nil, err }
		adapter := &tokenSourceAdapter{ts: ts}
		return microsoft.NewClient(ctx, adapter, user.Email)
	}
	return nil, fmt.Errorf("unknown provider: %s", provider)
}