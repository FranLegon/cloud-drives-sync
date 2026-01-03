package cmd

import (
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/spf13/cobra"
)

var freeMainCmd = &cobra.Command{
	Use:   "free-main",
	Short: "Transfer all files from main account to backup accounts",
	Long: `Moves all files from the main account's sync folder to backup accounts
with the most available space. Useful for freeing up space in the main account.`,
	RunE: runFreeMain,
}

func init() {
	rootCmd.AddCommand(freeMainCmd)
}

func runFreeMain(cmd *cobra.Command, args []string) error {
	runner := getTaskRunner()

	if err := requiresPreFlightCheck(runner); err != nil {
		return err
	}

	// TODO: Implement main account clearing logic
	logger.Info("Main account clearing not yet fully implemented")
	logger.Info("This would transfer all files from main account to backup accounts with most free space")

	return nil
}
