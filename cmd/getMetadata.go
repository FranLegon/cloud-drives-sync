package cmd

import (
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/spf13/cobra"
)

func runGetMetadata(cmd *cobra.Command, args []string) error {
	if err := sharedRunner.GetMetadata(); err != nil {
		return err
	}

	logger.Info("Metadata sync complete")
	return nil
}
