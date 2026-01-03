package cmd

import (
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
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
	runner := getTaskRunner()

	if err := requiresPreFlightCheck(runner); err != nil {
		return err
	}

	// Update metadata first
	logger.Info("Updating metadata...")
	if err := runner.GetMetadata(); err != nil {
		return err
	}

	// TODO: Implement cross-provider sync logic
	logger.Info("Cross-provider synchronization not yet fully implemented")
	logger.Info("This would compare files across providers using normalized paths and hashes")
	logger.Info("Missing files would be uploaded, conflicts would be renamed")

	return nil
}
