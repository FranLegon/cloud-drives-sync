package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var shareWithMainCmd = &cobra.Command{
	Use:   "share-with-main",
	Short: "Repair permissions for backup accounts",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("[Share-With-Main] Running pre-flight check...")
		if !preFlightCheckAllAccounts() {
			fmt.Println("Pre-flight check failed. Aborting.")
			return
		}
		fmt.Println("[Share-With-Main] Verifying permissions...")
		accounts := getAllAccounts()
		for _, acc := range accounts {
			if !accIsMain(acc) {
				if !hasEditorAccess(acc) {
					grantEditorAccess(acc)
					fmt.Printf("[Share-With-Main] Granted editor access to %s\n", acc.Email)
				}
			}
		}
		fmt.Println("[Share-With-Main] Permission repair complete.")
	},
}

func init() {
	rootCmd.AddCommand(shareWithMainCmd)
}

// Helper stubs
func accIsMain(acc interface{}) bool       { return false }
func hasEditorAccess(acc interface{}) bool { return false }
func grantEditorAccess(acc interface{})    {}
