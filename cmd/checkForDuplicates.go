package cmd

import (
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/spf13/cobra"
)

func runCheckForDuplicates(cmd *cobra.Command, args []string) error {
	// First, update metadata
	logger.Info("Updating metadata before checking for duplicates...")
	if err := sharedRunner.GetMetadata(); err != nil {
		return err
	}

	// Check for duplicates
	if err := sharedRunner.CheckForDuplicates(); err != nil {
		return err
	}

	return nil
}
