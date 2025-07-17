package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var removeDuplicatesUnsafeCmd = &cobra.Command{
	Use:   "remove-duplicates-unsafe",
	Short: "Automatically remove all but oldest duplicate files",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("[Remove-Duplicates-Unsafe] Checking for duplicates...")
		checkForDuplicatesCmd.Run(cmd, args)
		providers := []string{"Google", "Microsoft"}
		db := getDatabase()
		for _, provider := range providers {
			dupes, _ := db.GetDuplicates(provider)
			for _, files := range dupes {
				if len(files) < 2 {
					continue
				}
				oldest := files[0]
				for _, f := range files {
					if f.CreatedOn < oldest.CreatedOn {
						oldest = f
					}
				}
				for _, f := range files {
					if f.FileID != oldest.FileID {
						deleteFileFromCloud(f)
						fmt.Printf("Deleted duplicate: %s\n", f.FileName)
					}
				}
			}
		}
		fmt.Println("[Remove-Duplicates-Unsafe] Done.")
	},
}

func init() {
	rootCmd.AddCommand(removeDuplicatesUnsafeCmd)
}
