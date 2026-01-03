package cmd

import (
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/spf13/cobra"
)

var checkForDuplicatesCmd = &cobra.Command{
	Use:   "check-for-duplicates",
	Short: "Check for duplicate files within each provider",
	Long: `Scans the local database to find files with identical hashes
within the same cloud provider. Updates metadata first.`,
	RunE: runCheckForDuplicates,
}

func init() {
	rootCmd.AddCommand(checkForDuplicatesCmd)
}

func runCheckForDuplicates(cmd *cobra.Command, args []string) error {
	runner := getTaskRunner()

	// First, update metadata
	logger.Info("Updating metadata before checking for duplicates...")
	if err := requiresPreFlightCheck(runner); err != nil {
		return err
	}
	if err := runner.GetMetadata(); err != nil {
		return err
	}

	// Check for duplicates
	if err := runner.CheckForDuplicates(); err != nil {
		return err
	}

	return nil
}
