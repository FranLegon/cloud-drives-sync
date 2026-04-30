package cmd

import (
	"github.com/spf13/cobra"
)

var freeMainCmd = &cobra.Command{
	Use:   "free-main",
	Short: "Transfer all files from main account to backup accounts",
	Long: `Moves all files from the main account's sync folder to backup accounts
with the most available space. Useful for freeing up space in the main account.`,
	RunE: runFreeMain,
}

func init() {
	rootCmd.AddCommand(freeMainCmd)
}

func runFreeMain(cmd *cobra.Command, args []string) error {
	runner := getTaskRunner()

	if err := requiresPreFlightCheck(runner); err != nil {
		return err
	}

	if err := runner.FreeMain(); err != nil {
		return err
	}

	return nil
}
