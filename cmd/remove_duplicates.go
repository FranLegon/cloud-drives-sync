package cmd

import (
	"bufio"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var removeDuplicatesCmd = &cobra.Command{
	Use:   "remove-duplicates",
	Short: "Interactively remove duplicate files",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("[Remove-Duplicates] Checking for duplicates...")
		checkForDuplicatesCmd.Run(cmd, args)
		providers := []string{"Google", "Microsoft"}
		db := getDatabase()
		for _, provider := range providers {
			dupes, _ := db.GetDuplicates(provider)
			for hash, files := range dupes {
				fmt.Printf("[Duplicate][%s] Hash: %s\n", provider, hash)
				for i, f := range files {
					fmt.Printf("  [%d] File: %s | Owner: %s | Created: %s\n", i+1, f.FileName, f.OwnerEmail, f.CreatedOn)
				}
				fmt.Print("Select file(s) to delete (comma separated indices, or leave blank to skip): ")
				reader := bufio.NewReader(os.Stdin)
				input, _ := reader.ReadString('\n')
				indices := parseIndices(input)
				for _, idx := range indices {
					if idx > 0 && idx <= len(files) {
						deleteFileFromCloud(files[idx-1])
						fmt.Printf("Deleted file: %s\n", files[idx-1].FileName)
					}
				}
			}
		}
		fmt.Println("[Remove-Duplicates] Done.")
	},
}

func init() {
	rootCmd.AddCommand(removeDuplicatesCmd)
}

// Helper stub
func parseIndices(input string) []int   { return []int{} }
func deleteFileFromCloud(f interface{}) {}
