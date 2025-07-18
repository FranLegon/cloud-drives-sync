package cmd

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/config"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/model"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/task"
	"github.comcom/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/logger"
)

// removeDuplicatesCmd represents the remove-duplicates command
var removeDuplicatesCmd = &cobra.Command{
	Use:   "remove-duplicates",
	Short: "Interactively finds and removes duplicate files",
	Long: `This command first finds all sets of duplicate files within each provider,
identical to the 'check-for-duplicates' command.

For each set of duplicates found, it presents an interactive prompt asking you
to select which single file to KEEP. All other files in that set will then be
permanently deleted from the cloud provider and the local database.

You can also choose to skip any set of duplicates, leaving them untouched.`,
	Run: runRemoveDuplicates,
}

func runRemoveDuplicates(cmd *cobra.Command, args []string) {
	logger.Info("Starting interactive duplicate removal process...")

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

	// --- Step 2: Get Duplicates from DB ---
	ctx := context.Background()
	logger.Info("Updating metadata before checking for duplicates...")
	if err := runner.GetMetadata(ctx); err != nil {
		logger.Fatal("Failed during metadata retrieval: %v", err)
	}

	for _, provider := range []string{"Google", "Microsoft"} {
		logger.Info("\n--- Processing Provider: %s ---", provider)
		hashes, err := runner.DB.GetDuplicateHashes(provider)
		if err != nil {
			logger.Fatal("Failed to query for duplicate hashes for %s: %v", provider, err)
		}
		if len(hashes) == 0 {
			logger.Info("No duplicate files found for this provider.")
			continue
		}

		// --- Step 3: Interactive Deletion Loop ---
		for hash := range hashes {
			files, err := getAllFilesForHash(runner, provider, hash)
			if err != nil {
				logger.Fatal("Failed to get files by hash '%s': %v", hash, err)
			}
			if len(files) <= 1 {
				continue
			}

			// Prepare prompt items
			var items []string
			for _, f := range files {
				items = append(items, fmt.Sprintf("%s (Owner: %s, Modified: %s, Size: %d B)", f.FileName, f.OwnerEmail, f.LastModified.Format("2006-01-02"), f.FileSize))
			}
			const skipOption = ">> SKIP THIS SET (Keep All) <<"
			items = append(items, skipOption)

			prompt := promptui.Select{
				Label: fmt.Sprintf("Select file to KEEP for hash %s (all others will be deleted)", hash),
				Items: items,
				Size:  len(items),
			}

			selectedIndex, result, err := prompt.Run()
			if err != nil {
				// User pressed Ctrl+C, so we exit gracefully.
				logger.Info("Operation cancelled by user.")
				return
			}

			if result == skipOption {
				logger.Info("Skipping this set of duplicates.")
				continue
			}

			// --- Step 4: Perform Deletion ---
			fileToKeep := files[selectedIndex]
			logger.Info("Keeping file: %s", fileToKeep.FileName)

			for i, fileToDelete := range files {
				if i == selectedIndex {
					continue // This is the file we're keeping.
				}

				logger.Info("Deleting duplicate: %s (ID: %s)...", fileToDelete.FileName, fileToDelete.FileID)
				if safeRun {
					logger.TaggedInfo("DRY RUN", "Would delete file %s from account %s", fileToDelete.FileName, fileToDelete.OwnerEmail)
					continue
				}

				client, ok := runner.Clients[fileToDelete.OwnerEmail]
				if !ok {
					logger.Error("Client for owner %s not available. Cannot delete file %s. Please check token status.", fileToDelete.OwnerEmail, fileToDelete.FileName)
					continue
				}

				// Delete from cloud
				if err := client.DeleteItem(ctx, fileToDelete.FileID); err != nil {
					logger.Error("Failed to delete '%s' from cloud: %v", fileToDelete.FileName, err)
					continue
				}

				// Delete from local DB
				if err := runner.DB.DeleteFile(fileToDelete.Provider, fileToDelete.FileID); err != nil {
					logger.Error("Failed to delete '%s' from local database: %v", fileToDelete.FileName, err)
				}
			}
		}
	}
	logger.Info("\nDuplicate removal process complete.")
}

// getAllFilesForHash is a helper to query the DB for all files matching a hash, trying all known algorithms.
func getAllFilesForHash(runner *task.TaskRunner, provider, hash string) ([]model.File, error) {
	var allFiles []model.File

	for _, algo := range []string{"MD5", "quickXorHash", "SHA256"} {
		files, err := runner.DB.GetFilesByHash(provider, hash, algo)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) { // Check for specific errors if needed
				return nil, err
			}
			// For other errors, we can log them but try to continue
			logger.Error("Error querying for hash with algo %s: %v", algo, err)
		}
		if len(files) > 0 {
			allFiles = append(allFiles, files...)
		}
	}

	// Sort by modification date to provide a consistent order in the prompt
	sort.Slice(allFiles, func(i, j int) bool {
		return allFiles[i].LastModified.After(allFiles[j].LastModified)
	})

	return allFiles, nil
}
