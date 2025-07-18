package cmd

import (
	"context"
	"time"

	"cloud-drives-sync/internal/config"
	"cloud-drives-sync/internal/logger"
	"cloud-drives-sync/internal/task"

	"github.com/spf13/cobra"
)

// getMetadataCmd represents the get-metadata command
var getMetadataCmd = &cobra.Command{
	Use:   "get-metadata",
	Short: "Scans cloud accounts and updates the local metadata database",
	Long: `This command connects to all configured user accounts and performs a full scan
of the 'synched-cloud-drives' directory and all content shared with it.

It populates or updates a local, encrypted SQLite database with metadata for every
file and folder it finds. For files that do not have a provider-native content hash
(like Google Docs or Sheets), it will download an exported version (e.g., PDF) to
calculate a SHA-256 hash locally.

This ensures every file has a verifiable hash in the local database, which is
essential for de-duplication and synchronization commands.`,
	Run: runGetMetadata,
}

func runGetMetadata(cmd *cobra.Command, args []string) {
	start := time.Now()
	logger.Info("Starting metadata retrieval. This may take some time...")

	// --- Step 1: Initialize Task Runner ---
	masterPassword, err := config.GetMasterPassword(false)
	if err != nil {
		logger.Fatal("Failed to get master password: %v", err)
	}

	runner, err := task.NewTaskRunner(masterPassword, safeRun)
	if err != nil {
		logger.Fatal("Failed to initialize task runner: %v", err)
	}
	defer runner.Close()

	// --- Step 2: Execute the Metadata Task ---
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute) // 30-minute timeout for the whole process
	defer cancel()

	if err := runner.GetMetadata(ctx); err != nil {
		logger.Fatal("Failed during metadata retrieval: %v", err)
	}

	duration := time.Since(start)
	logger.Info("\nSuccessfully updated local metadata database in %s.", duration.Round(time.Second))
}
