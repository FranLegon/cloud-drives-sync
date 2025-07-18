package cmd

import (
	"bufio"
	"cloud-drives-sync/google"
	"cloud-drives-sync/microsoft"
	"fmt"
	"os"
	"strconv"
	"strings"

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

// Helper implementations

func parseIndices(input string) []int {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil
	}
	parts := strings.Split(input, ",")
	var indices []int
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if n, err := strconv.Atoi(p); err == nil {
			indices = append(indices, n)
		}
	}
	return indices
}

func deleteFileFromCloud(f interface{}) {
	file, ok := f.(struct {
		FileID     string
		Provider   string
		OwnerEmail string
	})
	if !ok {
		return
	}
	cfg, err := LoadConfig("")
	if err != nil {
		return
	}
	if file.Provider == "Google" {
		gd, _ := google.NewGoogleDrive(cfg.GoogleClient.ID, cfg.GoogleClient.Secret, getRefreshToken(cfg, file.Provider, file.OwnerEmail))
		_ = gd.DeleteFile(file.OwnerEmail, file.FileID)
	} else if file.Provider == "Microsoft" {
		ms, _ := microsoft.NewOneDrive(cfg.MicrosoftClient.ID, cfg.MicrosoftClient.Secret, getRefreshToken(cfg, file.Provider, file.OwnerEmail))
		_ = ms.DeleteFile(file.OwnerEmail, file.FileID)
	}
}
