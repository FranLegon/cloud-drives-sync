package cmd

import (
	"cloud-drives-sync/internal/logger"
	"cloud-drives-sync/internal/task"

	"github.com/spf13/cobra"
)

var syncProvidersCmd = &cobra.Command{
	Use:   "sync-providers",
	Short: "Synchronize files between Google and Microsoft main accounts",
	Long: `Ensures that files in the synched-cloud-drives folder are identical between
the main Google and Microsoft accounts. Missing files are copied, conflicts are renamed.`,
	RunE: runSyncProviders,
}

func runSyncProviders(cmd *cobra.Command, args []string) error {
	ctx := getContext()
	log := logger.New().WithPrefix("SYNC-PROVIDERS")

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
		log.Fatal("Failed to update metadata: %v", err)
	}

	// Sync providers
	if err := runner.SyncProviders(ctx, clients); err != nil {
		log.Fatal("Failed to sync providers: %v", err)
	}

	log.Success("Provider synchronization complete")
	return nil
}
