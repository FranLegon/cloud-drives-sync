package cmd

import (
	"github.com/spf13/cobra"
)

var helpCmd = &cobra.Command{
	Use:   "help",
	Short: "Show help for all commands",
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Root().Help()
	},
}

func init() {
	rootCmd.AddCommand(helpCmd)
}
