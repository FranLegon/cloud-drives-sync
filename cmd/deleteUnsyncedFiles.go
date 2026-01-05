package cmd

import (
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/spf13/cobra"
)

var deleteUnsyncedFilesCmd = &cobra.Command{
	Use:   "delete-unsynced-files",
	Short: "Delete files in backup accounts that are not in the sync folder",
	Long: `Iterates through all backup accounts and deletes any files or folders
found in the root directory that are not the designated 'sync-cloud-drives' folder.
This ensures backup accounts only contain synced data.`,
	RunE: runDeleteUnsyncedFiles,
}

func init() {
	rootCmd.AddCommand(deleteUnsyncedFilesCmd)
}

func runDeleteUnsyncedFiles(cmd *cobra.Command, args []string) error {
	runner := getTaskRunner()

	// Run pre-flight checks to ensure sync folders are identified correctly
	if err := requiresPreFlightCheck(runner); err != nil {
		return err
	}

	logger.Info("Starting cleanup of unsynced files...")
	if err := runner.DeleteUnsyncedFiles(); err != nil {
		return err
	}

	logger.Info("Cleanup complete")
	return nil
}
