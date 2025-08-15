package cmd

import (
	"cloud-drives-sync/internal/logger"

	"github.com/spf13/cobra"
)

var shareWithMainCmd = &cobra.Command{
	Use:   "share-with-main",
	Short: "Repairs folder permissions for backup accounts.",
	Long: `A utility command that verifies every backup account has 'editor' access
to its provider's main account 'synched-cloud-drives' folder. It re-applies
the permission if it is found to be missing. This is useful if a share was
revoked manually.`,
	Run: func(cmd *cobra.Command, args []string) {
		runner := setup()
		defer runner.DB.Close()

		if err := runner.ShareWithMain(); err != nil {
			logger.Error(err, "Permission repair failed")
		}
	},
}
