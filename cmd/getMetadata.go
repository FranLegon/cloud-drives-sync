package cmd

import (
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/spf13/cobra"
)

var getMetadataCmd = &cobra.Command{
	Use:   "get-metadata",
	Short: "Scan all cloud providers and update local metadata database",
	Long: `Recursively scans all configured cloud storage accounts and updates
the local encrypted database with file and folder metadata.`,
	RunE: runGetMetadata,
}

func init() {
	rootCmd.AddCommand(getMetadataCmd)
}

func runGetMetadata(cmd *cobra.Command, args []string) error {
	runner := getTaskRunner()

	if err := requiresPreFlightCheck(runner); err != nil {
		return err
	}

	if err := runner.GetMetadata(); err != nil {
		return err
	}

	logger.Info("Metadata sync complete")
	return nil
}
