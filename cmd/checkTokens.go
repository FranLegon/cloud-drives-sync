package cmd

import (
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/spf13/cobra"
)

var checkTokensCmd = &cobra.Command{
	Use:   "check-tokens",
	Short: "Check validity of all authentication tokens",
	Long: `Verifies that all refresh tokens and sessions are valid
without performing any actual synchronization.`,
	Annotations: map[string]string{
		"skipPreFlight": "true",
	},
	RunE: runCheckTokens,
}

func init() {
	rootCmd.AddCommand(checkTokensCmd)
}

func runCheckTokens(cmd *cobra.Command, args []string) error {
	if err := sharedRunner.CheckTokens(); err != nil {
		logger.Error("Token validation completed with errors")
		return err
	}

	logger.Info("All tokens are valid")
	return nil
}
