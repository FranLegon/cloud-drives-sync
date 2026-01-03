package cmd

import (
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/spf13/cobra"
)

var balanceStorageCmd = &cobra.Command{
	Use:   "balance-storage",
	Short: "Balance storage usage across accounts",
	Long: `Checks storage quotas and moves large files from over-quota accounts
to backup accounts with more free space within the same provider.`,
	RunE: runBalanceStorage,
}

func init() {
	rootCmd.AddCommand(balanceStorageCmd)
}

func runBalanceStorage(cmd *cobra.Command, args []string) error {
	runner := getTaskRunner()

	if err := requiresPreFlightCheck(runner); err != nil {
		return err
	}

	// Check quotas
	logger.Info("Checking storage quotas...")

	// TODO: Implement storage balancing logic
	logger.Info("Storage balancing not yet fully implemented")
	logger.Info("This would check quotas (95%% threshold) and move large files to accounts with more space")

	return nil
}
