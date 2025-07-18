package cmd

import (
	"context"
	"fmt"
	"sort"
	"time"

	"cloud-drives-sync/internal/config"
	"cloud-drives-sync/internal/logger"
	"cloud-drives-sync/internal/task"

	"github.com/spf13/cobra"
)

// checkForDuplicatesCmd represents the check-for-duplicates command
var checkForDuplicatesCmd = &cobra.Command{
	Use:   "check-for-duplicates",
	Short: "Finds and lists files with identical content within each provider",
	Long: `This command first runs the 'get-metadata' logic to ensure the local database
is synchronized with the state of your cloud accounts.

It then queries the local database to find all files that share an identical
content hash (e.g., MD5, quickXorHash, or SHA256) *within the same provider*.
The command prints a list of these duplicate sets, grouped by their content hash,
so you can identify redundant files.`,
	Run: runCheckForDuplicates,
}

func runCheckForDuplicates(cmd *cobra.Command, args []string) {
	start := time.Now()
	logger.Info("Checking for duplicate files...")

	// --- Step 1: Initialize Task Runner ---
	masterPassword, err := config.GetMasterPassword(false)
	if err != nil {
		logger.Fatal("Failed to get master password: %v", err)
	}
	runner, err := task.NewTaskRunner(masterPassword, safeRun)
	if err != nil {
		logger.Fatal("Failed to initialize task runner: %v", err)
	}
	defer runner.Close()

	// --- Step 2: Update Metadata ---
	ctx := context.Background()
	logger.Info("Updating metadata from all providers. This may take a moment...")
	if err := runner.GetMetadata(ctx); err != nil {
		logger.Fatal("Failed during metadata retrieval: %v", err)
	}

	// --- Step 3: Query and Display Duplicates ---
	logger.Info("\nQuerying database for duplicate files...")
	totalDuplicates := 0
	for _, provider := range []string{"Google", "Microsoft"} {
		logger.Info("\n--- Provider: %s ---", provider)

		// Get all hashes that appear more than once.
		hashes, err := runner.DB.GetDuplicateHashes(provider)
		if err != nil {
			logger.Fatal("Failed to query for duplicate hashes for %s: %v", provider, err)
		}

		if len(hashes) == 0 {
			logger.Info("No duplicate files found.")
			continue
		}

		totalDuplicates += len(hashes)

		// To ensure a consistent output order, we sort the hashes.
		sortedHashes := make([]string, 0, len(hashes))
		for h := range hashes {
			sortedHashes = append(sortedHashes, h)
		}
		sort.Strings(sortedHashes)

		for _, hash := range sortedHashes {
			// A single hash could belong to different algorithms, although unlikely.
			// We query for all known algorithms.
			files, err := runner.DB.GetFilesByHash(provider, hash, "MD5")
			if err != nil {
				logger.Fatal("Failed to get files by hash '%s': %v", hash, err)
			}
			qxorFiles, _ := runner.DB.GetFilesByHash(provider, hash, "quickXorHash")
			files = append(files, qxorFiles...)
			shaFiles, _ := runner.DB.GetFilesByHash(provider, hash, "SHA256")
			files = append(files, shaFiles...)

			if len(files) > 1 {
				fmt.Printf("\nFound %d duplicates for hash: %s\n", len(files), hash)
				for _, file := range files {
					fmt.Printf("  - Path: %s/%s (ID: %s, Owner: %s, Modified: %s)\n",
						provider, file.FileName, file.FileID, file.OwnerEmail, file.LastModified.Format("2006-01-02"))
				}
			}
		}
	}
	duration := time.Since(start)
	logger.Info("\nDuplicate check complete in %s. Found %d sets of duplicate files.", duration.Round(time.Second), totalDuplicates)
}
