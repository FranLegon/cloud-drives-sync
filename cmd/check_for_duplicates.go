package cmd

import (
	"cloud-drives-sync/database"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var checkDuplicatesCmd = &cobra.Command{
	Use:   "check-for-duplicates",
	Short: "Find duplicate files (by hash) within each provider",
	Run: func(cmd *cobra.Command, args []string) {
		exeDir, _ := os.Executable()
		exeDir = filepath.Dir(exeDir)
		dbPath := filepath.Join(exeDir, "metadata.db")
		db, err := database.OpenDB(dbPath)
		if err != nil {
			fmt.Printf("Failed to open DB: %v\n", err)
			os.Exit(1)
		}
		defer db.Close()
		dups, err := db.FindDuplicates()
		if err != nil {
			fmt.Printf("Error finding duplicates: %v\n", err)
			os.Exit(1)
		}
		if len(dups) == 0 {
			fmt.Println("No duplicates found.")
			return
		}
		for hash, files := range dups {
			fmt.Printf("Hash: %s\n", hash)
			for _, f := range files {
				fmt.Printf("  %s | %s | %s | %s\n", f.FileName, f.OwnerEmail, f.Provider, f.CreatedOn)
			}
		}
	},
}

func init() {
	rootCmd.AddCommand(checkDuplicatesCmd)
}
