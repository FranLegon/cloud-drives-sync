package cmd

import (
	"fmt"

	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/FranLegon/cloud-drives-sync/internal/model"
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

	// First, update metadata
	logger.Info("Updating metadata before checking for duplicates...")
	if err := requiresPreFlightCheck(runner); err != nil {
		return err
	}
	if err := runner.GetMetadata(); err != nil {
		return err
	}

	// Find duplicates
	providers := []model.Provider{model.ProviderGoogle, model.ProviderMicrosoft, model.ProviderTelegram}

	for _, provider := range providers {
		ids, err := db.GetDuplicateCalculatedIDs(provider)
		if err != nil {
			logger.ErrorTagged([]string{string(provider)}, "Failed to query duplicates: %v", err)
			continue
		}

		if len(ids) == 0 {
			logger.InfoTagged([]string{string(provider)}, "No duplicates found")
			continue
		}

		logger.InfoTagged([]string{string(provider)}, "Found %d duplicate file groups", len(ids))

		for _, id := range ids {
			files, err := db.GetFilesByCalculatedID(id, provider)
			if err != nil {
				continue
			}

			// Display files
			fmt.Printf("\n[%s] Duplicate files (CalculatedID: %s):\n", provider, id)
			var fileNames []string
			for i, file := range files {
				label := fmt.Sprintf("%d. %s (ID: %s, Size: %d, Created: %s)",
					i+1, file.Path, file.ID, file.Size, file.CreatedTime.Format("2006-01-02"))
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

			for {
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
				if !safeMode {
					client, err := runner.GetOrCreateClient(&model.User{
						Provider:     provider,
						Email:        file.UserEmail,
						Phone:        file.UserPhone,
						RefreshToken: "", // Will use from config
					})
					if err != nil {
						logger.ErrorTagged([]string{string(provider)}, "Failed to get client: %v", err)
						break
					}

					if err := client.DeleteFile(file.ID); err != nil {
						logger.ErrorTagged([]string{string(provider)}, "Failed to delete file: %v", err)
					} else {
						logger.InfoTagged([]string{string(provider)}, "Deleted file: %s", file.Path)
						db.DeleteFile(file.ID)
					}
				} else {
					logger.DryRunTagged([]string{string(provider)}, "Would delete file: %s (ID: %s)", file.Path, file.ID)
				}

				// Ask if more deletions
				continuePrompt := promptui.Prompt{
					Label:     "Delete another file from this group? (y/n)",
					Default:   "n",
					IsConfirm: true,
				}
				_, err = continuePrompt.Run()
				if err != nil {
					break
				}
			}
		}
	}

	logger.Info("Duplicate removal complete")
	return nil
}

func runRemoveDuplicatesUnsafe(cmd *cobra.Command, args []string) error {
	runner := getTaskRunner()

	// First, update metadata
	logger.Info("Updating metadata before checking for duplicates...")
	if err := requiresPreFlightCheck(runner); err != nil {
		return err
	}
	if err := runner.GetMetadata(); err != nil {
		return err
	}

	// Find and remove duplicates
	providers := []model.Provider{model.ProviderGoogle, model.ProviderMicrosoft, model.ProviderTelegram}

	totalDeleted := 0

	for _, provider := range providers {
		ids, err := db.GetDuplicateCalculatedIDs(provider)
		if err != nil {
			logger.ErrorTagged([]string{string(provider)}, "Failed to query duplicates: %v", err)
			continue
		}

		if len(ids) == 0 {
			logger.InfoTagged([]string{string(provider)}, "No duplicates found")
			continue
		}

		logger.InfoTagged([]string{string(provider)}, "Found %d duplicate file groups", len(ids))

		for _, id := range ids {
			files, err := db.GetFilesByCalculatedID(id, provider)
			if err != nil {
				continue
			}

			// Keep the oldest file, delete the rest
			if len(files) > 1 {
				oldestFile := files[0]
				for _, file := range files[1:] {
					if !safeMode {
						client, err := runner.GetOrCreateClient(&model.User{
							Provider:     provider,
							Email:        file.UserEmail,
							Phone:        file.UserPhone,
							RefreshToken: "", // Will use from config
						})
						if err != nil {
							logger.ErrorTagged([]string{string(provider)}, "Failed to get client: %v", err)
							continue
						}

						if err := client.DeleteFile(file.ID); err != nil {
							logger.ErrorTagged([]string{string(provider)}, "Failed to delete file: %v", err)
						} else {
							logger.InfoTagged([]string{string(provider)}, "Deleted duplicate: %s (kept %s)", file.Path, oldestFile.Path)
							db.DeleteFile(file.ID)
							totalDeleted++
						}
					} else {
						logger.DryRunTagged([]string{string(provider)}, "Would delete duplicate: %s (would keep %s)", file.Path, oldestFile.Path)
						totalDeleted++
					}
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
