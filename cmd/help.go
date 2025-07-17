package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var helpCmd = &cobra.Command{
	Use:   "help",
	Short: "Show help for all commands",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("cloud-drives-sync - Available Commands:")
		fmt.Println("  init                   Initialize configuration and database")
		fmt.Println("  add-account            Add a backup Google or Microsoft account")
		fmt.Println("  get-metadata           Scan and update local metadata from all cloud accounts")
		fmt.Println("  check-for-duplicates   Find duplicate files within each provider")
		fmt.Println("  remove-duplicates      Interactively remove duplicate files")
		fmt.Println("  remove-duplicates-unsafe Automatically remove all but oldest duplicate files")
		fmt.Println("  share-with-main        Repair permissions for backup accounts")
		fmt.Println("  sync-providers         Synchronize file content between main Google and Microsoft accounts")
		fmt.Println("  balance-storage        Balance storage usage across backup accounts")
		fmt.Println("  free-main              Transfer all files from main to backup accounts")
		fmt.Println("  check-tokens           Check validity of all refresh tokens")
		fmt.Println("  help                   Show help for all commands")
		fmt.Println("Flags:")
		fmt.Println("  --safe, -s             Dry run mode (no write/delete/permission changes)")
		fmt.Println("  --help, -h             Show detailed help for a command")
	},
}

func init() {
	rootCmd.AddCommand(helpCmd)
}
