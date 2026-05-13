package cmd

import (
	"github.com/spf13/cobra"
)

var freeMainCmd = &cobra.Command{
	Use:   "free-main",
	Short: "Free space on the main account",
	Long: `Moves files from the main account to backup accounts
to free up space. Only moves files that are not recently modified.`,
	Annotations: map[string]string{
		"writesDB": "true",
	},
	RunE: runFreeMain,
}

func init() {
	rootCmd.AddCommand(freeMainCmd)
}

func runFreeMain(cmd *cobra.Command, args []string) error {
	_, err := sharedRunner.FreeMain(0)
	return err
}
