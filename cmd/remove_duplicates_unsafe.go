package cmd

import (
	"cloud-drives-sync/database"
	"cloud-drives-sync/google"
	"cloud-drives-sync/microsoft"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
)

var removeDuplicatesUnsafeCmd = &cobra.Command{
	Use:   "remove-duplicates-unsafe",
	Short: "Automatically remove all but the oldest duplicate files",
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
		for _, files := range dups {
			if len(files) < 2 {
				continue
			}
			oldest := files[0]
			oldestTime, _ := time.Parse(time.RFC3339, oldest.CreatedOn)
			for _, f := range files[1:] {
				t, _ := time.Parse(time.RFC3339, f.CreatedOn)
				if t.Before(oldestTime) {
					oldest = f
					oldestTime = t
				}
			}
			for _, f := range files {
				if f.FileID == oldest.FileID {
					continue
				}
				if safeMode {
					SafeLog("DELETE %s file '%s' (FileID: %s) from account '%s'", f.Provider, f.FileName, f.FileID, f.OwnerEmail)
				} else {
					switch f.Provider {
					case "Google":
						google.DeleteFile(f)
					case "Microsoft":
						microsoft.DeleteFile(f)
					}
					db.DeleteFileRecord(f.FileID, f.Provider)
					fmt.Printf("Deleted %s from %s\n", f.FileName, f.OwnerEmail)
				}
			}
		}
	},
}

func init() {
	rootCmd.AddCommand(removeDuplicatesUnsafeCmd)
}
