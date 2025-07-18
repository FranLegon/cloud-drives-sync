package cmd

import (
	"context"
	"sort"

	"github.com/spf13/cobra"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/config"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/logger"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/task"
)

var removeDuplicatesUnsafeCmd = &cobra.Command{
	Use:   "remove-duplicates-unsafe",
	Short: "Automatically finds and removes duplicate files, keeping the oldest",
	Long: `This command performs the same duplicate-finding logic as 'check-for-duplicates'.
However, it does not prompt for user input. For each set of duplicate files, it
automatically determines which file has the oldest 'CreatedOn' date and keeps only
that one. All other newer duplicates are permanently deleted.

WARNING: This is a destructive operation. Use with caution. The '--safe' flag is
highly recommended for a dry run before committing to deletions.`,
	Run: runRemoveDuplicatesUnsafe,
}

func runRemoveDuplicatesUnsafe(cmd *cobra.Command, args []string) {
	logger.Info("Starting automatic duplicate removal process...")
	runner, err := task.NewTaskRunner(config.GetMasterPassword(false))
	if err != nil {
		logger.Fatal("Failed to initialize task runner: %v", err)
	}
	defer runner.Close()

	ctx := context.Background()
	if err := runner.GetMetadata(ctx); err != nil {
		logger.Fatal("Failed during metadata retrieval: %v", err)
	}

	for _, provider := range []string{"Google", "Microsoft"} {
		logger.Info("\n--- Processing Provider: %s ---", provider)
		hashes, err := runner.DB.GetDuplicateHashes(provider)
		if err != nil {
			logger.Fatal("Failed to query for duplicate hashes: %v", err)
		}

		for hash := range hashes {
			files, err := getAllFilesForHash(runner, provider, hash)
			if err != nil {
				logger.Fatal("Failed to get files by hash '%s': %v", hash, err)
			}
			if len(files) <= 1 {
				continue
			}

			sort.Slice(files, func(i, j int) bool { return files[i].CreatedOn.Before(files[j].CreatedOn) })
			fileToKeep := files[0]
			filesToDelete := files[1:]
			logger.Info("Found set for hash %s. Keeping oldest: '%s'.", hash, fileToKeep.FileName)

			for _, fileToDelete := range filesToDelete {
				logger.Info("Deleting newer duplicate: %s...", fileToDelete.FileName)
				if safeRun {
					logger.TaggedInfo("DRY RUN", "Would delete file %s", fileToDelete.FileName)
					continue
				}
				client := runner.Clients[fileToDelete.OwnerEmail]
				if err := client.DeleteItem(ctx, fileToDelete.FileID); err != nil {
					logger.Error("Failed to delete '%s' from cloud: %v", fileToDelete.FileName, err)
					continue
				}
				if err := runner.DB.DeleteFile(fileToDelete.Provider, fileToDelete.FileID); err != nil {
					logger.Error("Failed to delete '%s' from local DB: %v", fileToDelete.FileName, err)
				}
			}
		}
	}
	logger.Info("\nAutomatic duplicate removal complete.")
}
