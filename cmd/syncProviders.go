package cmd

import (
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/FranLegon/cloud-drives-sync/internal/task"
	"github.com/spf13/cobra"
)

var syncProvidersCmd = &cobra.Command{
	Use:   "sync-providers",
	Short: "Synchronize files across all cloud providers",
	Long: `Ensures file content is consistent across Google Drive, Microsoft OneDrive, and Telegram.
Uploads missing files and handles conflicts by renaming.`,
	RunE: runSyncProviders,
}

func init() {
	rootCmd.AddCommand(syncProvidersCmd)
}

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
