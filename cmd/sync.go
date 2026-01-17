package cmd

import (
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/FranLegon/cloud-drives-sync/internal/task"
	"github.com/spf13/cobra"
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Run full synchronization workflow",
	Long: `Runs a complete synchronization sequence:
1. quota: Check storage quotas
2. get-metadata: Update local metadata
3. free-main: Transfer files from main account to backup
4. remove-duplicates-unsafe (or remove-duplicates if --safe): Clean up duplicates
5. verbose sync-providers: Synchronize files across providers
6. balance-storage: Re-distribute files to balance usage across backup accounts`,
	RunE: runSync,
}

func init() {
	rootCmd.AddCommand(syncCmd)
}

func runSync(cmd *cobra.Command, args []string) error {
	// Create shared runner
	runner := getTaskRunner()

	// Run pre-flight checks once
	if err := requiresPreFlightCheck(runner); err != nil {
		return err
	}

	return SyncAction(runner, safeMode)
}

// SyncAction runs the full synchronization pipeline
func SyncAction(runner *task.Runner, isSafeMode bool) error {
	// 1. Quota
	logger.Info("[Step 1/6] Checking Quota...")
	if err := QuotaAction(runner); err != nil {
		return err
	}

	// 2. Get Metadata (The ONLY time we need to run full sync)
	logger.Info("[Step 2/6] Updating Metadata...")
	if err := runner.GetMetadata(); err != nil {
		return err
	}

	// 3. Free Main
	logger.Info("[Step 3/6] Freeing Main Account...")
	if err := runner.FreeMain(); err != nil {
		return err
	}

	// 4. Remove Duplicates
	logger.Info("[Step 4/6] Removing Duplicates...")
	if isSafeMode {
		// If safe mode, run interactive remove-duplicates (with false for metadata update)
		if err := RemoveDuplicatesAction(runner, false); err != nil {
			return err
		}
	} else {
		// Normal mode, run unsafe automatic removal (with false for metadata update)
		if err := RemoveDuplicatesUnsafeAction(runner, false); err != nil {
			return err
		}
	}

	// 5. Sync Providers
	logger.Info("[Step 5/6] Syncing Providers...")
	// Pass false to skip redundant metadata updates
	if err := SyncProvidersAction(runner, false); err != nil {
		return err
	}

	// 6. Balance Storage
	logger.Info("[Step 6/6] Balancing Storage...")
	if err := runner.BalanceStorage(); err != nil {
		return err
	}

	return nil
}
