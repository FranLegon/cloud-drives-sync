package cmd

import (
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/FranLegon/cloud-drives-sync/internal/task"
	"github.com/spf13/cobra"
)

var syncProvidersCmd = &cobra.Command{
	Use:   "sync-providers",
	Short: "Synchronize files across all providers",
	Long: `Synchronizes files across all configured cloud providers based on the local metadata database.
It handles new files, modifications, and deletions. By default, it updates metadata first.`,
	Annotations: map[string]string{
		"writesDB": "true",
	},
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
