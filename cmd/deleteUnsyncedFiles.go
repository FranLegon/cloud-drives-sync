package cmd

import (
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/spf13/cobra"
)

// runSyncUnsyncedFiles handles `sync --sync-unsynced-files`: it moves files sitting in a Google
// Drive backup account's actual root into cloud-drives-sync-root/cloud-drives-sync-aux/unsynced-from-backups.
func runSyncUnsyncedFiles(cmd *cobra.Command, args []string) error {
	logger.Info("Moving unsynced files from backup account roots...")
	if err := sharedRunner.MoveUnsyncedFiles(); err != nil {
		return err
	}

	logger.Info("Unsynced-files move complete")
	return nil
}
