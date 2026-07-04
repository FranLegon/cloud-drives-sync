//go:build auto

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	autoFlag    bool
	setFlag     bool
	disableFlag bool
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage configuration (auto build)",
	Long: `Manage the scheduled synchronization for the deployment (auto) build.

Only the --auto action is available in auto builds:
  --auto --set       Install the recurring scheduled sync (requires --password)
  --auto --disable   Remove the recurring scheduled sync`,
	Annotations: map[string]string{
		"skipSetup":        "true",
		"autoBuildAllowed": "true",
	},
	RunE: runConfig,
}

func init() {
	configCmd.Flags().BoolVar(&autoFlag, "auto", false, "Manage the recurring scheduled sync")
	configCmd.Flags().BoolVar(&setFlag, "set", false, "With --auto: create the scheduled task/service")
	configCmd.Flags().BoolVarP(&disableFlag, "disable", "d", false, "With --auto: remove the scheduled task/service")
	rootCmd.AddCommand(configCmd)
}

func runConfig(cmd *cobra.Command, args []string) error {
	if !autoFlag {
		return fmt.Errorf("only 'config --auto' is available in auto builds")
	}
	return runAutoAction()
}
