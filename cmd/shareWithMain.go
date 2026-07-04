package cmd

import (
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/spf13/cobra"
)

func runShareWithMain(cmd *cobra.Command, args []string) error {
	if err := sharedRunner.ShareWithMain(); err != nil {
		return err
	}

	logger.Info("Share permissions verified")
	return nil
}
