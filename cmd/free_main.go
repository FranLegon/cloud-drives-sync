package cmd

import (
	"context"
	"time"

	"github.com/spf13/cobra"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/config"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/logger"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/task"
)

// freeMainCmd represents the free-main command
var freeMainCmd = &cobra.Command{
	Use:   "free-main",
	Short: "Transfers all files from a main account to its backup accounts",
	Long: `This command is designed to empty the 'synched-cloud-drives' directory of a main account
by moving all of its files to the associated backup accounts of the same provider.

For each file in the main account, it identifies the backup account with the most
available free space and moves the file there. The command will first check if the
combined free space of all backup accounts is sufficient for the transfer. If not, it
will abort with an error before moving any files.

Like other move operations, it attempts a native ownership transfer first before falling
back to a download/upload/delete process.`,
	Run: runFreeMain,
}

func runFreeMain(cmd *cobra.Command, args []string) {
	start := time.Now()
	logger.Info("Starting process to free main account storage...")

	// --- Step 1: Initialize Task Runner ---
	masterPassword, err := config.GetMasterPassword(false)
	if err != nil {
		logger.Fatal("Failed to get master password: %v", err)
	}

	// Because this command's logic is self-contained in the TaskRunner,
	// we instantiate it and call the dedicated method.
	runner, err := task.NewTaskRunner(masterPassword, safeRun)
	if err != nil {
		logger.Fatal("Failed to initialize task runner: %v", err)
	}
	defer runner.Close()

	// --- Step 2: Execute the Free Main Storage Task ---
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour) // Moving many files can take a long time
	defer cancel()

	if err := runner.FreeMainStorage(ctx); err != nil {
		logger.Fatal("Failed to free main account storage: %v", err)
	}

	duration := time.Since(start)
	if safeRun {
		logger.Info("\n[DRY RUN] 'Free main' dry run complete in %s.", duration.Round(time.Second))
	} else {
		logger.Info("\n'Free main' process complete in %s.", duration.Round(time.Second))
	}
}
