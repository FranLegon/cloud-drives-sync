package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var checkTokensCmd = &cobra.Command{
	Use:   "check-tokens",
	Short: "Check validity of all refresh tokens",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("[Check-Tokens] Checking all refresh tokens...")
		accounts := getAllAccounts()
		for _, acc := range accounts {
			if !tokenIsValid(acc) {
				fmt.Printf("[Check-Tokens] Token invalid for %s. Please re-authenticate using add-account.\n", acc.Email)
			}
		}
		fmt.Println("[Check-Tokens] Done.")
	},
}

func init() {
	rootCmd.AddCommand(checkTokensCmd)
}

// Helper stub
func tokenIsValid(acc interface{}) bool { return true }
