package cmd

import (
	"cloud-drives-sync/internal/logger"
	"cloud-drives-sync/internal/task"

	"github.com/spf13/cobra"
)

var checkTokensCmd = &cobra.Command{
	Use:   "check-tokens",
	Short: "Validate all authentication tokens",
	Long:  `Tests each refresh token to ensure it can still authenticate successfully.`,
	RunE:  runCheckTokens,
}

func runCheckTokens(cmd *cobra.Command, args []string) error {
	ctx := getContext()
	log := logger.New().WithPrefix("CHECK-TOKENS")

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

	// Create clients
	clients, err := createAllClients(ctx, cfg)
	if err != nil {
		log.Fatal("Failed to create clients: %v", err)
	}

	// Create task runner
	runner := task.NewTaskRunner(cfg, db, password, safeMode)

	// Check tokens
	if err := runner.CheckTokens(ctx, clients); err != nil {
		log.Fatal("Failed to check tokens: %v", err)
	}

	log.Success("Token validation complete")
	return nil
}
