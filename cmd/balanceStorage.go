package cmd

import (
	"cloud-drives-sync/internal/logger"

	"github.com/spf13/cobra"
)

var balanceStorageCmd = &cobra.Command{
	Use:   "balance-storage",
	Short: "Moves files from full accounts to backups.",
	Long: `Checks storage quotas for all accounts. If an account is over 95% full,
it moves the largest files from it to a backup account of the same provider
that has more available space, continuing until the source account is below 90% full.`,
	Run: func(cmd *cobra.Command, args []string) {
		runner := setup()
		defer runner.DB.Close()

		logger.Info("Balancing storage...")
		if err := runner.BalanceStorage(); err != nil {
			logger.Error(err, "Storage balancing failed")
		}
		logger.Info("Storage balancing complete.")
	},
}
