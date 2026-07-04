package cmd

import (
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/spf13/cobra"
)

func runCheckTokens(cmd *cobra.Command, args []string) error {
	if err := sharedRunner.CheckTokens(); err != nil {
		logger.Error("Token validation completed with errors")
		return err
	}

	logger.Info("All tokens are valid")
	return nil
}
