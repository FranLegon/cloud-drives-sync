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
1. quota: Check storage quotas (and update metadata)
2. free-main: Transfer files from main account to backup
3. remove-duplicates-unsafe (or remove-duplicates if --safe): Clean up duplicates
4. verbose sync-providers: Synchronize files across providers
5. balance-storage: Re-distribute files to balance usage across backup accounts`,
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
	logger.Info("[Step 1/5] Checking Quota...")
	if err := QuotaAction(runner); err != nil {
		return err
	}

	// 2. Free Main
	logger.Info("[Step 2/5] Freeing Main Account...")
	moved, err := runner.FreeMain()
	if err != nil {
		return err
	}

	// 3. Remove Duplicates
	logger.Info("[Step 3/5] Removing Duplicates...")
	var deleted int
	if isSafeMode {
		// If safe mode, run interactive remove-duplicates (with false for metadata update)
		if err := RemoveDuplicatesAction(runner, false); err != nil {
			return err
		}
	} else {
		// Normal mode, run unsafe automatic removal (with false for metadata update)
		deleted, err = RemoveDuplicatesUnsafeAction(runner, false)
		if err != nil {
			return err
		}
	}

	// 4. Sync Providers
	logger.Info("[Step 4/5] Syncing Providers...")
	// Only refresh metadata if FreeMain or RemoveDuplicates actually changed files,
	// otherwise the DB from Step 1 is still accurate.
	needsMetadataRefresh := moved > 0 || deleted > 0 || isSafeMode
	if !needsMetadataRefresh {
		logger.Info("Skipping metadata refresh (no files moved or deleted)")
	}
	if err := SyncProvidersAction(runner, needsMetadataRefresh); err != nil {
		return err
	}

	// 5. Balance Storage
	logger.Info("[Step 5/5] Balancing Storage...")
	if err := runner.BalanceStorage(); err != nil {
		return err
	}

	return nil
}
