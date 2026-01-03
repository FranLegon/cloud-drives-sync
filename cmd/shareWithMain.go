package cmd

import (
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/spf13/cobra"
)

var shareWithMainCmd = &cobra.Command{
	Use:   "share-with-main",
	Short: "Verify and repair share permissions with main accounts",
	Long: `Ensures that all backup accounts have proper permissions to access
the main account's sync folders. Re-applies permissions if missing.`,
	RunE: runShareWithMain,
}

func init() {
	rootCmd.AddCommand(shareWithMainCmd)
}

func runShareWithMain(cmd *cobra.Command, args []string) error {
	runner := getTaskRunner()

	if err := requiresPreFlightCheck(runner); err != nil {
		return err
	}

	if err := runner.ShareWithMain(); err != nil {
		return err
	}

	logger.Info("Share permissions verified")
	return nil
}
