package cmd

import (
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/spf13/cobra"
)

var getMetadataCmd = &cobra.Command{
	Use:   "get-metadata",
	Short: "Update local metadata from cloud providers",
	Long: `Fetches the latest file metadata (names, IDs, hashes, sizes)
from all configured cloud providers and updates the local database.`,
	Annotations: map[string]string{
		"writesDB": "true",
	},
	RunE: runGetMetadata,
}

func init() {
	rootCmd.AddCommand(getMetadataCmd)
}

func runGetMetadata(cmd *cobra.Command, args []string) error {
	if err := sharedRunner.GetMetadata(); err != nil {
		return err
	}

	logger.Info("Metadata sync complete")
	return nil
}
