package cmd

import (
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/FranLegon/cloud-drives-sync/internal/task"
	"github.com/spf13/cobra"
)

func runSyncProviders(cmd *cobra.Command, args []string) error {
	return SyncProvidersAction(sharedRunner, true, 0)
}

// SyncProvidersAction runs the sync logic with optional metadata update.
// syncRunID is used for copy checkpointing; pass 0 to disable checkpointing.
func SyncProvidersAction(runner *task.Runner, updateMetadata bool, syncRunID int64) error {
	if updateMetadata {
		// Update metadata first
		logger.Info("Updating metadata...")
		if err := runner.GetMetadata(); err != nil {
			return err
		}
	}

	return runner.SyncProviders(syncRunID)
}
