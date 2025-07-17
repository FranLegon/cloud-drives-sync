package cmd

import (
	"fmt"
	"os"
	"cloud-drives-sync/database"
	"github.com/spf13/cobra"
)

var getMetadataCmd = &cobra.Command{
	Use:   "get-metadata",
	Short: "Scan and update local metadata from all cloud accounts",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("[Get-Metadata] Running pre-flight check...")
		if !preFlightCheckAllAccounts() {
			fmt.Println("Pre-flight check failed. Aborting.")
			os.Exit(1)
		}
		fmt.Println("[Get-Metadata] Scanning files in synched-cloud-drives folders...")
		accounts := getAllAccounts()
		db := getDatabase()
		for _, acc := range accounts {
			files := listFilesInSyncFolder(acc)
			for _, f := range files {
				hash := calculateSHA256(f)
				fileRecord := database.FileRecord{
					FileID:           f.ID,
					Provider:         acc.Provider,
					OwnerEmail:       acc.Email,
					FileHash:         hash,
					FileName:         f.Name,
					FileSize:         f.Size,
					FileExtension:    getFileExtension(f.Name),
					ParentFolderID:   f.ParentID,
					ParentFolderName: "synched-cloud-drives",
					CreatedOn:        f.Created,
					LastModified:     f.Modified,
					LastSynced:       getCurrentTimestamp(),
				}
				db.InsertOrUpdateFile(fileRecord)
				fmt.Printf("[Account: %s] Synced file: %s\n", acc.Email, f.Name)
			}
		}
		fmt.Println("[Get-Metadata] Metadata update complete.")
	},
}

func init() {
	rootCmd.AddCommand(getMetadataCmd)
}

// Helper stubs for integration
func preFlightCheckAllAccounts() bool { return true }
func getAllAccounts() []struct{Provider, Email string} { return []struct{Provider, Email string}{}}
// ...existing code...
func getDatabase() database.Database {
	db := &database.SQLiteDB{}
	err := db.InitDB("bin/metadata.db")
	if err != nil {
		fmt.Println("[Database] Failed to initialize DB:", err)
		return nil
	}
	return db
}
func listFilesInSyncFolder(acc struct{Provider, Email string}) []struct{ID, Name, ParentID, Created, Modified string; Size int64} { return nil }
func calculateSHA256(f struct{ID, Name, ParentID, Created, Modified string; Size int64}) string { return "" }
func getFileExtension(name string) string { return "" }
func getCurrentTimestamp() string { return "" }
