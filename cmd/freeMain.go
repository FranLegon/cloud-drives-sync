package cmd

import (
	"cloud-drives-sync/internal/logger"

	"github.com/spf13/cobra"
)

var freeMainCmd = &cobra.Command{
	Use:   "free-main",
	Short: "Moves all files from a main account to its backups.",
	Long: `Transfers all files from a selected main account's 'synched-cloud-drives'
folder to its associated backup accounts (of the same provider), distributing
them based on available space.`,
	Run: func(cmd *cobra.Command, args []string) {
		runner := setup()
		defer runner.DB.Close()

		logger.Info("Freeing main account storage...")
		if err := runner.FreeMain(); err != nil {
			logger.Error(err, "Free main operation failed")
		}
	},
}
