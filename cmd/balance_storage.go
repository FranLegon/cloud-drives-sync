package cmd

import (
	"context"
	"time"

	"cloud-drives-sync/internal/config"
	"cloud-drives-sync/internal/logger"
	"cloud-drives-sync/internal/task"

	"github.com/spf13/cobra"
)

// balanceStorageCmd represents the balance-storage command
var balanceStorageCmd = &cobra.Command{
	Use:   "balance-storage",
	Short: "Balances storage usage between main and backup accounts",
	Long: `This command helps manage storage quotas across your accounts for a single provider.
It works as follows:

1. It checks the storage quota for every configured account (main and backup).
2. If any account is over 95% full, it identifies the largest files within that account's
   'synched-cloud-drives' folder (or shared content).
3. It then moves these files, one by one, to a backup account of the *same provider*
   that has the most available free space.
4. This process continues until the source account's usage drops below a 90% threshold.

The command will first attempt a native API ownership transfer for the move. If that fails
or is unsupported, it will fall back to a download/re-upload/delete process.`,
	Run: runBalanceStorage,
}

func runBalanceStorage(cmd *cobra.Command, args []string) {
	start := time.Now()
	logger.Info("Starting storage balancing process...")

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

	// --- Step 2: Execute the Balance Storage Task ---
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour) // Moving large files can take time
	defer cancel()

	if err := runner.BalanceStorage(ctx); err != nil {
		logger.Fatal("Storage balancing failed: %v", err)
	}

	duration := time.Since(start)
	if safeRun {
		logger.Info("\n[DRY RUN] Storage balancing dry run complete in %s.", duration.Round(time.Second))
	} else {
		logger.Info("\nStorage balancing complete in %s.", duration.Round(time.Second))
	}
}
