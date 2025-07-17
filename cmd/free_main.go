package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var freeMainCmd = &cobra.Command{
	Use:   "free-main",
	Short: "Transfer all files from main to backup accounts",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("[Free-Main] Running pre-flight check...")
		if !preFlightCheckAllAccounts() {
			fmt.Println("Pre-flight check failed. Aborting.")
			return
		}
		fmt.Println("[Free-Main] Transferring files from main to backup accounts...")
		mainAccounts := getMainAccounts()
		for _, main := range mainAccounts {
			files := getFilesOwnedByMain(main)
			for _, f := range files {
				backup := getBackupWithMostSpace(main.Provider)
				if backup == nil {
					fmt.Println("No backup account with enough space.")
					break
				}
				if !enoughSpace(backup, f) {
					fmt.Printf("Not enough space in backup %s for file %s\n", backup.Email, f.FileName)
					continue
				}
				if canTransferOwnership(main, backup) {
					transferOwnership(f, main, backup)
					fmt.Printf("Transferred ownership of %s to %s\n", f.FileName, backup.Email)
				} else {
					downloadAndReupload(f, main, backup)
					fmt.Printf("Re-uploaded %s to %s\n", f.FileName, backup.Email)
				}
			}
		}
		fmt.Println("[Free-Main] Done.")
	},
}

func init() {
	rootCmd.AddCommand(freeMainCmd)
}

// Helper stubs
func getMainAccounts() []struct{ Provider, Email string }              { return nil }
func getFilesOwnedByMain(main interface{}) []struct{ FileName string } { return nil }
func enoughSpace(backup, f interface{}) bool                           { return true }
func canTransferOwnership(main, backup interface{}) bool               { return true }
func transferOwnership(f, main, backup interface{})                    {}
func downloadAndReupload(f, main, backup interface{})                  {}
