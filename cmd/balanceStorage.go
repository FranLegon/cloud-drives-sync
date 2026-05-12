package cmd

import (
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/spf13/cobra"
)

var balanceStorageCmd = &cobra.Command{
	Use:   "balance-storage",
	Short: "Balance storage across all accounts",
	Long: `Re-distributes files across all configured accounts to ensure
even storage usage across providers. Updates metadata first.`,
	Annotations: map[string]string{
		"writesDB": "true",
	},
	RunE: runBalanceStorage,
}

func init() {
	rootCmd.AddCommand(balanceStorageCmd)
}

func runBalanceStorage(cmd *cobra.Command, args []string) error {
	// Check quotas
	logger.Info("Checking storage quotas...")
	return sharedRunner.BalanceStorage()
}
