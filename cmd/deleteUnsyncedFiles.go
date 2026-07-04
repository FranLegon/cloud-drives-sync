package cmd

import (
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/spf13/cobra"
)

// runSyncUnsyncedFiles handles `sync --sync-unsynced-files`.
// TODO(Phase 2): per SPEC this must MOVE Google backup root files into
// cloud-drives-sync-aux/unsynced-from-backups rather than delete them.
func runSyncUnsyncedFiles(cmd *cobra.Command, args []string) error {
	logger.Info("Starting cleanup of unsynced files...")
	if err := sharedRunner.DeleteUnsyncedFiles(); err != nil {
		return err
	}

	logger.Info("Cleanup complete")
	return nil
}
