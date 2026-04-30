package cmd

import (
	"fmt"

	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/FranLegon/cloud-drives-sync/internal/task"
	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
)

var removeDuplicatesCmd = &cobra.Command{
	Use:   "remove-duplicates",
	Short: "Interactively remove duplicate files",
	Long: `Finds duplicate files and prompts the user to select which ones to delete.
This command runs in interactive mode and requires user confirmation for each deletion.`,
	RunE: runRemoveDuplicates,
}

var removeDuplicatesUnsafeCmd = &cobra.Command{
	Use:   "remove-duplicates-unsafe",
	Short: "Automatically remove duplicate files (keeps oldest)",
	Long: `Finds duplicate files and automatically deletes all copies except the one
with the oldest creation date. Use with caution!`,
	RunE: runRemoveDuplicatesUnsafe,
}

func init() {
	rootCmd.AddCommand(removeDuplicatesCmd)
	rootCmd.AddCommand(removeDuplicatesUnsafeCmd)
}

func runRemoveDuplicates(cmd *cobra.Command, args []string) error {
	runner := getTaskRunner()

	if err := requiresPreFlightCheck(runner); err != nil {
		return err
	}

	return RemoveDuplicatesAction(runner, true)
}

// RemoveDuplicatesAction runs interactive duplicate removal
func RemoveDuplicatesAction(runner *task.Runner, updateMetadata bool) error {
	if updateMetadata {
		// First, update metadata
		logger.Info("Updating metadata before checking for duplicates...")
		if err := runner.GetMetadata(); err != nil {
			return err
		}
	}

	// Find duplicates
	// Note: In normalized schema, duplicates are at file level, not provider level
	ids, err := db.GetDuplicateCalculatedIDs()
	if err != nil {
		logger.Error("Failed to query duplicates: %v", err)
		return err
	}

	if len(ids) == 0 {
		logger.Info("No duplicates found")
		return nil
	}

	logger.Info("Found %d duplicate file groups", len(ids))

	for _, id := range ids {
		files, err := db.GetFilesByCalculatedID(id)
		if err != nil {
			continue
		}

		for {
			if len(files) <= 1 {
				break
			}

			// Display files
			fmt.Printf("\nDuplicate files (CalculatedID: %s):\n", id)
			var fileNames []string
			for i, file := range files {
				providerList := []string{}
				for _, replica := range file.Replicas {
					providerList = append(providerList, string(replica.Provider))
				}
				label := fmt.Sprintf("%d. %s (ID: %s, Size: %d, ModTime: %s, Providers: %v)",
					i+1, file.Path, file.ID, file.Size, file.ModTime.Format("2006-01-02"), providerList)
				fmt.Println("  " + label)
				fileNames = append(fileNames, label)
			}

			// Add "Skip" option
			fileNames = append(fileNames, "Skip this group")

			// Prompt for deletion
			selectPrompt := promptui.Select{
				Label: "Select file(s) to DELETE",
				Items: fileNames,
			}

			idx, _, err := selectPrompt.Run()
			if err != nil {
				break
			}

			if idx >= len(files) {
				// Skip selected
				break
			}

			// Delete the selected file
			file := files[idx]
			deleted := false
			if !safeMode {
				// Delete all replicas of this file
				for _, replica := range file.Replicas {
					client, err := getClientForReplica(runner, replica)
					if err != nil {
						logger.ErrorTagged([]string{string(replica.Provider)}, "Failed to get client: %v", err)
						continue
					}

					if err := client.DeleteFile(replica.NativeID); err != nil {
						logger.ErrorTagged([]string{string(replica.Provider)}, "Failed to delete file: %v", err)
					} else {
						logger.InfoTagged([]string{string(replica.Provider)}, "Deleted replica: %s", file.Path)
					}
				}
				db.DeleteFile(file.ID)
				deleted = true
			} else {
				logger.DryRun("Would delete file: %s (ID: %s)", file.Path, file.ID)
				deleted = true
			}

			if deleted {
				// Remove from files slice
				files = append(files[:idx], files[idx+1:]...)
			}
		}
	}

	logger.Info("Duplicate removal complete")
	return nil
}

func runRemoveDuplicatesUnsafe(cmd *cobra.Command, args []string) error {
	runner := getTaskRunner()

	if err := requiresPreFlightCheck(runner); err != nil {
		return err
	}

	return RemoveDuplicatesUnsafeAction(runner, true)
}

func RemoveDuplicatesUnsafeAction(runner *task.Runner, updateMetadata bool) error {
	if updateMetadata {
		// First, update metadata
		logger.Info("Updating metadata before checking for duplicates...")
		if err := runner.GetMetadata(); err != nil {
			return err
		}
	}

	// Find and remove duplicates
	ids, err := db.GetDuplicateCalculatedIDs()
	if err != nil {
		logger.Error("Failed to query duplicates: %v", err)
		return err
	}

	if len(ids) == 0 {
		logger.Info("No duplicates found")
		return nil
	}

	totalDeleted := 0

	logger.Info("Found %d duplicate file groups", len(ids))

	for _, id := range ids {
		files, err := db.GetFilesByCalculatedID(id)
		if err != nil {
			continue
		}

		// Keep the oldest file (by ModTime), delete the rest
		if len(files) > 1 {
			// Sort by ModTime to find oldest
			oldestIdx := 0
			for i, file := range files {
				if file.ModTime.Before(files[oldestIdx].ModTime) {
					oldestIdx = i
				}
			}
			oldestFile := files[oldestIdx]

			for i, file := range files {
				if i == oldestIdx {
					continue // Skip the oldest file
				}

				if !safeMode {
					// Delete all replicas of this file
					for _, replica := range file.Replicas {
						client, err := getClientForReplica(runner, replica)
						if err != nil {
							logger.ErrorTagged([]string{string(replica.Provider)}, "Failed to get client: %v", err)
							continue
						}

						if err := client.DeleteFile(replica.NativeID); err != nil {
							logger.ErrorTagged([]string{string(replica.Provider)}, "Failed to delete file: %v", err)
						} else {
							logger.InfoTagged([]string{string(replica.Provider)}, "Deleted duplicate replica: %s (kept %s)", file.Path, oldestFile.Path)
						}
					}
					db.DeleteFile(file.ID)
					totalDeleted++
				} else {
					logger.DryRun("Would delete duplicate: %s (would keep %s)", file.Path, oldestFile.Path)
					totalDeleted++
				}
			}
		}
	}

	if safeMode {
		logger.Info("Would have deleted %d duplicate files", totalDeleted)
	} else {
		logger.Info("Deleted %d duplicate files", totalDeleted)
	}
	return nil
}
