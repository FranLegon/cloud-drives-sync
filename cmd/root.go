package cmd

import (
	"cloud-drives-sync/internal/api"
	"cloud-drives-sync/internal/auth"
	"cloud-drives-sync/internal/config"
	"cloud-drives-sync/internal/database"
	"cloud-drives-sync/internal/google"
	"cloud-drives-sync/internal/logger"
	"cloud-drives-sync/internal/microsoft"
	"cloud-drives-sync/internal/model"
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
		client, err := createTempClient(user.Provider, cfg, user)
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
}

// getEmailFromToken creates a temporary client just to get the user's email for configuration.
func getEmailFromToken(provider string, cfg *config.Config, refreshToken string) (string, error) {
	tempUser := model.User{Email: "temp", RefreshToken: refreshToken, Provider: provider}
	client, err := createTempClient(provider, cfg, tempUser)
	if err != nil {
		return "", err
	}
	return client.GetUserEmail()
}

// createTempClient is a helper for creating a single-use client.
func createTempClient(provider string, cfg *config.Config, user model.User) (api.CloudClient, error) {
	ctx := context.Background()
	switch provider {
	case "Google":
		ts, err := auth.GetGoogleTokenSource(ctx, cfg, user.RefreshToken)
		if err != nil {
			return nil, err
		}
		return google.NewClient(ctx, ts, user.Email)
	case "Microsoft":
		ts, err := auth.GetMicrosoftTokenSource(ctx, cfg, user.RefreshToken)
		if err != nil {
			return nil, err
		}
		adapter := &tokenSourceAdapter{ts: ts}
		return microsoft.NewClient(ctx, adapter, user.Email)
	}
	return nil, fmt.Errorf("unknown provider: %s", provider)
}
