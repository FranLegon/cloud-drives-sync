package cmd

import (
	"cloud-drives-sync/internal/logger"

	"github.com/spf13/cobra"
)

var checkForDuplicatesCmd = &cobra.Command{
	Use:   "check-for-duplicates",
	Short: "Finds files with identical content within the same provider.",
	Long: `First, this command updates the local metadata to ensure it's current.
Then, it queries the database to find all files that have the exact same content hash.
It prints a list of these duplicate files, grouped by their hash, for user review.`,
	Run: func(cmd *cobra.Command, args []string) {
		runner := setup()
		defer runner.DB.Close()

		logger.Info("Checking for duplicates...")
		if _, err := runner.CheckForDuplicates(); err != nil {
			logger.Error(err, "Failed to check for duplicates")
		}
	},
}
