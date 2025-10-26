package cmd

import (
	"cloud-drives-sync/internal/logger"
	"cloud-drives-sync/internal/task"

	"github.com/spf13/cobra"
)

var removeDuplicatesCmd = &cobra.Command{
	Use:   "remove-duplicates",
	Short: "Remove duplicate files (keeping oldest)",
	Long: `Finds duplicate files and automatically removes all copies except the oldest one.
Use --safe flag to preview changes without making them.`,
	RunE: runRemoveDuplicates,
}

func runRemoveDuplicates(cmd *cobra.Command, args []string) error {
	ctx := getContext()
	log := logger.New().WithPrefix("REMOVE-DUPLICATES")

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

	// Update metadata
	log.Info("Updating metadata...")
	if err := runner.GetMetadata(ctx, clients); err != nil {
		log.Error("Failed to update metadata: %v", err)
	}

	// Find duplicates
	duplicates, err := runner.CheckForDuplicates(ctx)
	if err != nil {
		log.Fatal("Failed to check for duplicates: %v", err)
	}

	// Remove duplicates
	if err := runner.RemoveDuplicatesUnsafe(ctx, clients, duplicates); err != nil {
		log.Fatal("Failed to remove duplicates: %v", err)
	}

	log.Success("Duplicate removal complete")
	return nil
}
