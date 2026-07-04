package cmd

import (
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/FranLegon/cloud-drives-sync/internal/task"
	"github.com/spf13/cobra"
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Operate and maintain the pool",
	Long: `With no action flag, runs the full synchronization workflow in order:
  sync-unsynced-files -> quota -> free-main -> remove-duplicates
  -> sync-providers -> balance-storage

Provide exactly one action flag to run only that operation.`,
	Annotations: map[string]string{
		"writesDB":         "true",
		"autoBuildAllowed": "true",
	},
	RunE: runSync,
}

func init() {
	registerSyncActionFlags(syncCmd)
	rootCmd.AddCommand(syncCmd)
}

func runSync(cmd *cobra.Command, args []string) error {
	handled, err := dispatchSyncAction(cmd)
	if err != nil {
		return err
	}
	if handled {
		return nil
	}
	return SyncAction(sharedRunner, safeMode)
}

// SyncAction runs the full synchronization pipeline
func SyncAction(runner *task.Runner, isSafeMode bool) error {
	// Check for an interrupted previous run to resume
	startStep := 1
	var syncRunID int64

	prevRun, err := db.GetIncompleteSyncRun()
	if err != nil {
		logger.Warning("Failed to check for incomplete sync run: %v", err)
	}

	if prevRun != nil && prevRun.SafeMode == isSafeMode {
		syncRunID = prevRun.ID
		startStep = prevRun.LastCompletedStep + 1
		logger.Info("Resuming interrupted sync run #%d from step %d/5", syncRunID, startStep)
	} else {
		syncRunID, err = db.CreateSyncRun(isSafeMode)
		if err != nil {
			return err
		}
	}

	// 1. Quota
	if startStep <= 1 {
		logger.Info("[Step 1/5] Checking Quota...")
		if err := QuotaAction(runner, true); err != nil {
			return err
		}
		if err := db.MarkStepCompleted(syncRunID, 1); err != nil {
			logger.Warning("Failed to checkpoint step 1: %v", err)
		}
	} else {
		logger.Info("[Step 1/5] Skipping Quota (already completed)")
	}

	// 2. Free Main
	if startStep <= 2 {
		logger.Info("[Step 2/5] Freeing Main Account...")
		_, err := runner.FreeMain(syncRunID)
		if err != nil {
			return err
		}
		if err := db.MarkStepCompleted(syncRunID, 2); err != nil {
			logger.Warning("Failed to checkpoint step 2: %v", err)
		}
	} else {
		logger.Info("[Step 2/5] Skipping Free Main (already completed)")
	}

	// 3. Remove Duplicates
	if startStep <= 3 {
		logger.Info("[Step 3/5] Removing Duplicates...")
		if isSafeMode {
			if err := RemoveDuplicatesAction(runner, false); err != nil {
				return err
			}
		} else {
			if err := RemoveDuplicatesUnsafeAction(runner, false); err != nil {
				return err
			}
		}
		if err := db.MarkStepCompleted(syncRunID, 3); err != nil {
			logger.Warning("Failed to checkpoint step 3: %v", err)
		}
	} else {
		logger.Info("[Step 3/5] Skipping Remove Duplicates (already completed)")
	}

	// 4. Sync Providers
	if startStep <= 4 {
		logger.Info("[Step 4/5] Syncing Providers...")
		if err := SyncProvidersAction(runner, true, syncRunID); err != nil {
			return err
		}
		if err := db.MarkStepCompleted(syncRunID, 4); err != nil {
			logger.Warning("Failed to checkpoint step 4: %v", err)
		}
	} else {
		logger.Info("[Step 4/5] Skipping Sync Providers (already completed)")
	}

	// 5. Balance Storage
	if startStep <= 5 {
		logger.Info("[Step 5/5] Balancing Storage...")
		if err := runner.BalanceStorage(syncRunID); err != nil {
			return err
		}
		if err := db.MarkStepCompleted(syncRunID, 5); err != nil {
			logger.Warning("Failed to checkpoint step 5: %v", err)
		}
	} else {
		logger.Info("[Step 5/5] Skipping Balance Storage (already completed)")
	}

	// Mark run fully completed
	if err := db.CompleteSyncRun(syncRunID); err != nil {
		logger.Warning("Failed to mark sync run as completed: %v", err)
	}

	// Housekeeping: remove old completed sync runs
	if err := db.CleanupOldSyncRuns(5); err != nil {
		logger.Warning("Failed to cleanup old sync runs: %v", err)
	}

	return nil
}
