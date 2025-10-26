package cmd

import (
	"cloud-drives-sync/internal/api"
	"cloud-drives-sync/internal/auth"
	"cloud-drives-sync/internal/google"
	"cloud-drives-sync/internal/logger"
	"cloud-drives-sync/internal/microsoft"
	"cloud-drives-sync/internal/model"
	"cloud-drives-sync/internal/task"

	"github.com/spf13/cobra"
)

var getMetadataCmd = &cobra.Command{
	Use:   "get-metadata",
	Short: "Scan all accounts and update the local database",
	Long: `Recursively scans the synched-cloud-drives folder in all configured accounts
and updates the local metadata database with file and folder information.`,
	RunE: runGetMetadata,
}

func runGetMetadata(cmd *cobra.Command, args []string) error {
	ctx := getContext()
	log := logger.New().WithPrefix("GET-METADATA")

	// Load config and database
	cfg, password, err := loadConfig()
	if err != nil {
		log.Fatal("Failed to load config: %v", err)
	}

	db, err := openDatabase(password)
	if err != nil {
		log.Fatal("Failed to open database: %v", err)
	}
	defer db.Close()

	// Create clients for all users
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

		oauthConfig, _ := auth.OAuthConfig(user.Provider, clientID, clientSecret)
		tokenSource := auth.TokenSource(ctx, oauthConfig, user.RefreshToken)

		var client api.CloudClient
		if user.Provider == model.ProviderGoogle {
			client, err = google.NewClient(ctx, tokenSource)
		} else {
			client, err = microsoft.NewClient(ctx, tokenSource)
		}

		if err != nil {
			log.Error("Failed to create client for %s: %v", user.Email, err)
			continue
		}

		clients[user.Email] = client
	}

	// Create task runner
	runner := task.NewTaskRunner(cfg, db, password, safeMode)

	// Run pre-flight checks
	if err := runner.PreflightCheck(ctx, clients); err != nil {
		log.Fatal("Pre-flight check failed: %v", err)
	}

	// Get metadata
	if err := runner.GetMetadata(ctx, clients); err != nil {
		log.Fatal("Failed to get metadata: %v", err)
	}

	log.Success("Metadata updated successfully")
	return nil
}
