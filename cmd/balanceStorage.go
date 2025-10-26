package cmd

import (
	"cloud-drives-sync/internal/logger"
	"cloud-drives-sync/internal/task"

	"github.com/spf13/cobra"
)

var balanceStorageCmd = &cobra.Command{
	Use:   "balance-storage",
	Short: "Balance storage usage across accounts within each provider",
	Long: `Checks storage quotas for all accounts. If any account is over 95% full,
moves large files to backup accounts until usage is below 90%.`,
	RunE: runBalanceStorage,
}

func runBalanceStorage(cmd *cobra.Command, args []string) error {
	ctx := getContext()
	log := logger.New().WithPrefix("BALANCE-STORAGE")

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

	// Run pre-flight checks
	if err := runner.PreflightCheck(ctx, clients); err != nil {
		log.Fatal("Pre-flight check failed: %v", err)
	}

	// Update metadata
	log.Info("Updating metadata...")
	if err := runner.GetMetadata(ctx, clients); err != nil {
		log.Error("Failed to update metadata: %v", err)
	}

	// Balance storage
	if err := runner.BalanceStorage(ctx, clients); err != nil {
		log.Fatal("Failed to balance storage: %v", err)
	}

	log.Success("Storage balancing complete")
	return nil
}
