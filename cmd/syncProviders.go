package cmd

import (
	"cloud-drives-sync/internal/logger"

	"github.com/spf13/cobra"
)

var syncProvidersCmd = &cobra.Command{
	Use:   "sync-providers",
	Short: "Mirrors content between main Google and Microsoft accounts.",
	Long: `Ensures that the file content within 'synched-cloud-drives' is identical
between the main accounts of each provider. It uploads missing files and renames
conflicting files (same path, different content hash).`,
	Run: func(cmd *cobra.Command, args []string) {
		runner := setup()
		defer runner.DB.Close()

		if err := runner.SyncProviders(); err != nil {
			logger.Error(err, "Provider sync failed")
		}
	},
}
