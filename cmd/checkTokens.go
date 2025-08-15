package cmd

import (
	"cloud-drives-sync/internal/logger"

	"github.com/spf13/cobra"
)

var checkTokensCmd = &cobra.Command{
	Use:   "check-tokens",
	Short: "Verifies all stored account tokens are still valid.",
	Long: `Iterates through all configured accounts and makes a simple, read-only API call
to check if the stored refresh tokens are still valid. Reports on any tokens
that have expired or been revoked and require re-authentication.`,
	Run: func(cmd *cobra.Command, args []string) {
		runner := setup()
		defer runner.DB.Close()

		logger.Info("Checking token validity for all configured accounts...")
		runner.CheckTokens()
		logger.Info("Token check complete.")
	},
}
