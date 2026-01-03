package cmd

import (
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/spf13/cobra"
)

var checkTokensCmd = &cobra.Command{
	Use:   "check-tokens",
	Short: "Validate all authentication tokens",
	Long: `Checks that all stored refresh tokens are valid and can be used
to access the cloud providers. Reports any expired or revoked tokens.`,
	RunE: runCheckTokens,
}

func init() {
	rootCmd.AddCommand(checkTokensCmd)
}

func runCheckTokens(cmd *cobra.Command, args []string) error {
	runner := getTaskRunner()

	if err := runner.CheckTokens(); err != nil {
		logger.Error("Token validation completed with errors")
		return err
	}

	logger.Info("All tokens are valid")
	return nil
}
