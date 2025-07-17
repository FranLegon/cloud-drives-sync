package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var balanceStorageCmd = &cobra.Command{
	Use:   "balance-storage",
	Short: "Balance storage usage across backup accounts",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("[Balance-Storage] Running pre-flight check...")
		if !preFlightCheckAllAccounts() {
			fmt.Println("Pre-flight check failed. Aborting.")
			return
		}
		fmt.Println("[Balance-Storage] Checking quotas...")
		accounts := getAllAccounts()
		for _, acc := range accounts {
			used, total := getQuota(acc)
			if float64(used)/float64(total) > 0.95 {
				fmt.Printf("[Balance-Storage] %s is over 95%% full. Balancing...\n", acc.Email)
				files := getLargestFiles(acc)
				for _, f := range files {
					if !fileInOtherAccounts(f, acc) {
						backup := getBackupWithMostSpace(acc.Provider)
						if backup == nil {
							fmt.Println("No backup account with enough space.")
							break
						}
						transferOwnershipOrMove(f, acc, backup)
						fmt.Printf("Moved %s to %s\n", f.FileName, backup.Email)
					}
					used2, total2 := getQuota(acc)
					if float64(used2)/float64(total2) < 0.90 {
						break
					}
				}
			}
		}
		fmt.Println("[Balance-Storage] Done.")
	},
}

func init() {
	rootCmd.AddCommand(balanceStorageCmd)
}

// Helper stubs
func getQuota(acc interface{}) (int64, int64)                        { return 0, 0 }
func getLargestFiles(acc interface{}) []struct{ FileName string }    { return nil }
func fileInOtherAccounts(f interface{}, acc interface{}) bool        { return false }
func getBackupWithMostSpace(provider string) *struct{ Email string } { return nil }
func transferOwnershipOrMove(f, from, to interface{})                {}
