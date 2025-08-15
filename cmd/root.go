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
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
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
}

// Execute adds all child commands to the root command and sets flags appropriately.
func Execute() error {
	return rootCmd.Execute()
}

func init() {
	rootCmd.PersistentFlags().BoolVarP(&safeRun, "safe", "s", false, "Perform a dry run without making remote changes (--safe)")
	rootCmd.AddCommand(initCmd, addAccountCmd, getMetadataCmd, checkForDuplicatesCmd, removeDuplicatesCmd, removeDuplicatesUnsafeCmd, syncProvidersCmd, balanceStorageCmd, freeMainCmd, checkTokensCmd, shareWithMainCmd)
}

func promptForPassword(label string) string {
	prompt := promptui.Prompt{
		Label: label,
		Mask:  '*',
	}
	result, err := prompt.Run()
	if err != nil {
		logger.Info("Operation cancelled.")
		os.Exit(0)
	}
	return result
}

// tokenSourceAdapter adapts an oauth2.TokenSource to the azcore.TokenCredential interface.
type tokenSourceAdapter struct {
	ts oauth2.TokenSource
}

// GetToken satisfies the azcore.TokenCredential interface.
func (t *tokenSourceAdapter) GetToken(_ context.Context, _ policy.TokenRequestOptions) (azcore.AccessToken, error) {
	token, err := t.ts.Token()
	if err != nil {
		return azcore.AccessToken{}, err
	}
	return azcore.AccessToken{
		Token:     token.AccessToken,
		ExpiresOn: token.Expiry,
	}, nil
}

// setup is the main helper function called by operational commands.
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
	for _, user := range cfg.Users {
		client, err := createClient(user.Provider, cfg, user)
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

// getEmailFromToken creates a temporary client to get a user's email from a new token.
func getEmailFromToken(provider string, cfg *config.Config, refreshToken string) (string, error) {
	tempUser := model.User{Email: "temp", RefreshToken: refreshToken, Provider: provider}
	client, err := createClient(provider, cfg, tempUser)
	if err != nil {
		return "", err
	}
	return client.GetUserEmail()
}

// createClient is a factory for API clients.
func createClient(provider string, cfg *config.Config, user model.User) (api.CloudClient, error) {
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
