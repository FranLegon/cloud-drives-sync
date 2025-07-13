package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var safeMode bool

var rootCmd = &cobra.Command{
	Use:   "cloud-drives-sync",
	Short: "Synchronize and deduplicate files across Google Drive and OneDrive accounts",
	Long:  `A robust CLI tool to manage, deduplicate, and synchronize files across multiple Google Drive and OneDrive accounts, confined to the synched-cloud-drives folder.`,
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Help()
	},
}

func Execute() error {
	rootCmd.PersistentFlags().BoolVar(&safeMode, "safe", false, "Dry run: print actions instead of executing them")
	return rootCmd.Execute()
}

func SafeLog(format string, a ...interface{}) {
	if safeMode {
		fmt.Printf("[DRY RUN] "+format+"\n", a...)
	}
}

func MustNotSafe() {
	if safeMode {
		fmt.Println("This operation cannot be performed in --safe mode.")
		os.Exit(1)
	}
}
