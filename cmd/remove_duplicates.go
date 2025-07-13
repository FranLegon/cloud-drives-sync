package cmd

import (
	"bufio"
	"cloud-drives-sync/database"
	"cloud-drives-sync/google"
	"cloud-drives-sync/microsoft"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var removeDuplicatesCmd = &cobra.Command{
	Use:   "remove-duplicates",
	Short: "Interactively remove duplicate files",
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
		reader := bufio.NewReader(os.Stdin)
		for hash, files := range dups {
			fmt.Printf("Hash: %s\n", hash)
			for i, f := range files {
				fmt.Printf("  [%d] %s | %s | %s | %s\n", i, f.FileName, f.OwnerEmail, f.Provider, f.CreatedOn)
			}
			fmt.Print("Enter indices to delete (comma separated), or leave blank to skip: ")
			resp, _ := reader.ReadString('\n')
			resp = strings.TrimSpace(resp)
			if resp == "" {
				continue
			}
			indices := strings.Split(resp, ",")
			for _, idxStr := range indices {
				idxStr = strings.TrimSpace(idxStr)
				idx := -1
				fmt.Sscanf(idxStr, "%d", &idx)
				if idx >= 0 && idx < len(files) {
					f := files[idx]
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
		}
	},
}

func init() {
	rootCmd.AddCommand(removeDuplicatesCmd)
}
