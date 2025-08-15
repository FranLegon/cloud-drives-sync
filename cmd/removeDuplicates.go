package cmd

import (
	"cloud-drives-sync/internal/logger"
	"fmt"

	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
)

var removeDuplicatesCmd = &cobra.Command{
	Use:   "remove-duplicates",
	Short: "Interactively removes duplicate files.",
	Long: `Finds sets of duplicate files and, for each set, prompts the user
to select which single file to KEEP. All other files in that set will be
moved to the trash.`,
	Run: func(cmd *cobra.Command, args []string) {
		runner := setup()
		defer runner.DB.Close()

		duplicates, err := runner.CheckForDuplicates()
		if err != nil {
			logger.Error(err, "Failed to find duplicates")
		}
		if len(duplicates) == 0 {
			return // No duplicates were found.
		}

		logger.Info("\nStarting interactive duplicate resolution...")
		for hashKey, files := range duplicates {
			fmt.Printf("\n--- Resolving duplicates for hash: %s ---\n", hashKey)

			// Customize the prompt template for better readability
			templates := &promptui.SelectTemplates{
				Label:    "{{ . }}?",
				Active:   "-> {{ .Path | cyan }} (Owner: {{ .OwnerEmail }})",
				Inactive: "   {{ .Path | white }} (Owner: {{ .OwnerEmail }})",
				Selected: "-> Keeping {{ .Path | green }}",
			}

			prompt := promptui.Select{
				Label:     "Select the one file to KEEP. All others will be deleted",
				Items:     files,
				Templates: templates,
				Size:      10,
			}

			i, _, err := prompt.Run()
			if err != nil {
				logger.Warn("", nil, "Skipping this set of duplicates due to prompt cancellation.")
				continue
			}

			// Iterate through the files again, deleting all except the selected one.
			for j, fileToDelete := range files {
				if i == j {
					continue // This is the one we're keeping.
				}
				if runner.IsSafeRun {
					logger.DryRun(fileToDelete.OwnerEmail, "DELETE file '%s'", fileToDelete.Path)
					continue
				}

				client := runner.Clients[fileToDelete.OwnerEmail]
				logger.TaggedInfo(fileToDelete.OwnerEmail, "Deleting '%s'", fileToDelete.Path)
				if err := client.DeleteFile(fileToDelete.FileID); err != nil {
					logger.Warn(fileToDelete.OwnerEmail, err, "failed to delete file")
				} else {
					// Also remove it from our local database on successful deletion.
					runner.DB.DeleteFile(fileToDelete.FileID, fileToDelete.Provider)
				}
			}
		}
		logger.Info("Interactive duplicate resolution complete.")
	},
}

var removeDuplicatesUnsafeCmd = &cobra.Command{
	Use:   "remove-duplicates-unsafe",
	Short: "Automatically removes all but the oldest duplicate file.",
	Long: `Finds all duplicate files and, for each set, automatically deletes all
copies except for the one with the oldest creation date.
Use with caution. The --safe flag is highly recommended for the first run.`,
	Run: func(cmd *cobra.Command, args []string) {
		runner := setup()
		defer runner.DB.Close()

		logger.Info("Starting unsafe duplicate removal...")
		if err := runner.RemoveDuplicatesUnsafe(); err != nil {
			logger.Error(err, "Failed during unsafe duplicate removal")
		}
		logger.Info("Unsafe duplicate removal process complete.")
	},
}
