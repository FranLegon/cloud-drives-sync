package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var checkForDuplicatesCmd = &cobra.Command{
	Use:   "check-for-duplicates",
	Short: "Find duplicate files within each provider",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("[Check-For-Duplicates] Updating metadata...")
		getMetadataCmd.Run(cmd, args)
		fmt.Println("[Check-For-Duplicates] Searching for duplicates...")
		providers := []string{"Google", "Microsoft"}
		db := getDatabase()
		for _, provider := range providers {
			dupes, err := db.GetDuplicates(provider)
			if err != nil {
				fmt.Printf("[Error][%s] %v\n", provider, err)
				continue
			}
			for hash, files := range dupes {
				fmt.Printf("[Duplicate][%s] Hash: %s\n", provider, hash)
				for _, f := range files {
					fmt.Printf("  File: %s | Owner: %s | Created: %s\n", f.FileName, f.OwnerEmail, f.CreatedOn)
				}
			}
		}
		fmt.Println("[Check-For-Duplicates] Done.")
	},
}

func init() {
	rootCmd.AddCommand(checkForDuplicatesCmd)
}
