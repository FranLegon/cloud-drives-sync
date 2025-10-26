package cmd

import (
	"context"
	"fmt"
	"os"

	"cloud-drives-sync/internal/api"
	"cloud-drives-sync/internal/auth"
	"cloud-drives-sync/internal/config"
	"cloud-drives-sync/internal/database"
	"cloud-drives-sync/internal/google"
	"cloud-drives-sync/internal/logger"
	"cloud-drives-sync/internal/microsoft"
	"cloud-drives-sync/internal/model"

	"github.com/spf13/cobra"
)

var (
	safeMode bool
	log      *logger.Logger
)

// rootCmd represents the base command
var rootCmd = &cobra.Command{
	Use:   "cloud-drives-sync",
	Short: "Synchronize and manage files across Google Drive and OneDrive accounts",
	Long: `cloud-drives-sync is a command-line tool for managing and synchronizing
files across multiple Google Drive and OneDrive accounts. It provides
de-duplication, cross-provider sync, and storage balancing capabilities.`,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		log = logger.New()
	},
}

// Execute runs the root command
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	// Global flags
	rootCmd.PersistentFlags().BoolVarP(&safeMode, "safe", "s", false, "Dry-run mode: show what would be done without making changes")

	// Add all subcommands
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(addAccountCmd)
	rootCmd.AddCommand(getMetadataCmd)
	rootCmd.AddCommand(checkForDuplicatesCmd)
	rootCmd.AddCommand(removeDuplicatesCmd)
	rootCmd.AddCommand(syncProvidersCmd)
	rootCmd.AddCommand(balanceStorageCmd)
	rootCmd.AddCommand(freeMainCmd)
	rootCmd.AddCommand(checkTokensCmd)
	rootCmd.AddCommand(shareWithMainCmd)
}

// getPassword prompts for and returns the master password
func getPassword() (string, error) {
	fmt.Print("Enter master password: ")
	var password string
	_, err := fmt.Scanln(&password)
	if err != nil {
		return "", err
	}
	return password, nil
}

// loadConfig loads the configuration with password
func loadConfig() (*config.Config, string, error) {
	password, err := getPassword()
	if err != nil {
		return nil, "", fmt.Errorf("failed to read password: %w", err)
	}

	cfg, err := config.Load(password)
	if err != nil {
		return nil, "", fmt.Errorf("failed to load config: %w", err)
	}

	return cfg, password, nil
}

// openDatabase opens the database with password
func openDatabase(password string) (database.Database, error) {
	db, err := database.NewDatabase(password)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}
	return db, nil
}

// getContext returns a background context
func getContext() context.Context {
	return context.Background()
}

// createAllClients creates API clients for all users in the config
func createAllClients(ctx context.Context, cfg *config.Config) (map[string]api.CloudClient, error) {
	clients := make(map[string]api.CloudClient)

	for _, user := range cfg.Users {
		var clientID, clientSecret string
		if user.Provider == model.ProviderGoogle {
			clientID = cfg.GoogleClient.ID
			clientSecret = cfg.GoogleClient.Secret
		} else {
			clientID = cfg.MicrosoftClient.ID
			clientSecret = cfg.MicrosoftClient.Secret
		}

		oauthConfig, err := auth.OAuthConfig(user.Provider, clientID, clientSecret)
		if err != nil {
			return nil, fmt.Errorf("failed to create OAuth config for %s: %w", user.Email, err)
		}

		tokenSource := auth.TokenSource(ctx, oauthConfig, user.RefreshToken)

		var client api.CloudClient
		if user.Provider == model.ProviderGoogle {
			client, err = google.NewClient(ctx, tokenSource)
		} else {
			client, err = microsoft.NewClient(ctx, tokenSource)
		}

		if err != nil {
			return nil, fmt.Errorf("failed to create client for %s: %w", user.Email, err)
		}

		clients[user.Email] = client
	}

	return clients, nil
}
