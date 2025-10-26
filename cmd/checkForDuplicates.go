package cmd

import (
	"fmt"

	"cloud-drives-sync/internal/logger"
	"cloud-drives-sync/internal/task"

	"github.com/spf13/cobra"
)

var checkForDuplicatesCmd = &cobra.Command{
	Use:   "check-for-duplicates",
	Short: "Find duplicate files within each provider",
	Long:  `Scans the database for files with identical hashes within each provider and lists them.`,
	RunE:  runCheckForDuplicates,
}

func runCheckForDuplicates(cmd *cobra.Command, args []string) error {
	ctx := getContext()
	log := logger.New().WithPrefix("CHECK-DUPLICATES")

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

	// Create task runner
	runner := task.NewTaskRunner(cfg, db, password, safeMode)

	// First, update metadata
	clients, err := createAllClients(ctx, cfg)
	if err != nil {
		log.Fatal("Failed to create clients: %v", err)
	}

	log.Info("Updating metadata...")
	if err := runner.GetMetadata(ctx, clients); err != nil {
		log.Error("Failed to update metadata: %v", err)
	}

	// Check for duplicates
	duplicates, err := runner.CheckForDuplicates(ctx)
	if err != nil {
		log.Fatal("Failed to check for duplicates: %v", err)
	}

	// Display results
	for provider, dups := range duplicates {
		fmt.Printf("\n=== %s Duplicates ===\n", provider)
		for hash, files := range dups {
			fmt.Printf("\nHash: %s\n", hash)
			for _, file := range files {
				fmt.Printf("  - %s (ID: %s, Owner: %s, Created: %s, Size: %d bytes)\n",
					file.FileName, file.FileID, file.OwnerEmail, file.CreatedOn.Format("2006-01-02"), file.FileSize)
			}
		}
	}

	return nil
}
