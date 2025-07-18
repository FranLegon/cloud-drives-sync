package cmd

import (
	"context"
	"time"

	"github.com/spf13/cobra"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/config"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/logger"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/task"
)

// syncProvidersCmd represents the sync-providers command
var syncProvidersCmd = &cobra.Command{
	Use:   "sync-providers",
	Short: "Mirrors files between the main Google and Microsoft accounts",
	Long: `This command ensures that the content within the 'synched-cloud-drives' folder
is identical between the main Google and main Microsoft accounts.

It performs a two-way synchronization:
1. It first ensures the local metadata is up-to-date.
2. It compares the file sets of the two main accounts using their normalized paths and content hashes.
3. If a file exists on one provider but not the other, it is copied over, replicating the directory structure.
4. If a file exists in the same path on both providers but with a different content hash, it is considered
   a conflict. The incoming file is uploaded with a '_conflict_YYYY-MM-DD' suffix to prevent data loss.

This command requires a configured main account for both Google and Microsoft to function.`,
	Run: runSyncProviders,
}

func runSyncProviders(cmd *cobra.Command, args []string) {
	start := time.Now()
	logger.Info("Starting two-way provider synchronization. This may take a long time and transfer a lot of data.")

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

	// --- Step 2: Execute the Sync Task ---
	// This can be a very long process, so we use a long timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()

	if err := runner.SyncProviders(ctx); err != nil {
		logger.Fatal("Provider synchronization failed: %v", err)
	}

	duration := time.Since(start)
	if safeRun {
		logger.Info("\n[DRY RUN] Provider synchronization dry run complete in %s.", duration.Round(time.Second))
	} else {
		logger.Info("\nProvider synchronization complete in %s.", duration.Round(time.Second))
	}
}
