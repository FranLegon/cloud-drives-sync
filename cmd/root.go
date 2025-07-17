package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "cloud-drives-sync",
	Short: "Synchronize files across Google Drive and OneDrive accounts",
	Long:  `cloud-drives-sync manages and synchronizes files across multiple Google Drive and OneDrive accounts, de-duplicates files, mirrors data, and balances storage usage.`,
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Help()
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
