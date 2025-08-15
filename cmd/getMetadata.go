package cmd

import (
	"cloud-drives-sync/internal/logger"

	"github.com/spf13/cobra"
)

var getMetadataCmd = &cobra.Command{
	Use:   "get-metadata",
	Short: "Fetches and stores metadata for all files and folders.",
	Long: `Recursively scans the 'synched-cloud-drives' folder in each main account.
It populates the local encrypted database with metadata about every file and
folder, calculating content hashes where necessary. This is the first step
before running most other commands.`,
	Run: func(cmd *cobra.Command, args []string) {
		runner := setup()
		defer runner.DB.Close()

		logger.Info("Starting metadata retrieval. This may take some time for large drives...")
		if err := runner.GetMetadata(); err != nil {
			logger.Error(err, "Failed to get metadata")
		}
		logger.Info("Metadata retrieval complete.")
	},
}
