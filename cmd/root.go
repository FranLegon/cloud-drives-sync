package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

// safeRun is a persistent flag that, when true, prevents any write/delete/modify
// operations against the cloud provider APIs.
var safeRun bool

// rootCmd represents the base command when called without any subcommands.
// It is the entry point for the entire CLI application.
var rootCmd = &cobra.Command{
	Use:   "cloud-drives-sync",
	Short: "A tool to manage and synchronize files across Google Drive and OneDrive accounts.",
	Long: `cloud-drives-sync is a command-line tool designed to de-duplicate files,
ensure data is mirrored across providers, and balance storage usage between a
primary 'main' account and one or more 'backup' accounts per provider.

All operations are contained within a specific folder named 'synched-cloud-drives'
in each main account to ensure safety and prevent accidental data loss.

Configuration and user authentication tokens are stored locally in encrypted files,
requiring a master password to run any command.`,
	// PersistentPreRun is used to suppress the default help command output on error,
	// allowing for cleaner error messages from logger.Fatal.
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		// This space can be used for global setup before any command runs,
		// such as initializing a global logger configuration if needed.
	},
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is the main function called by main.go. It will parse the command-line
// arguments and execute the appropriate command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		// Cobra prints the error, so we just exit with a non-zero status code.
		os.Exit(1)
	}
}

// init function is called by Go when the package is initialized.
// It sets up the command structure, adding commands and flags.
func init() {
	// Add the persistent '--safe' or '-s' flag to the root command.
	// This makes it available to all child commands.
	rootCmd.PersistentFlags().BoolVarP(&safeRun, "safe", "s", false, "Perform a dry run without making changes to cloud providers")

	// Add all other command definitions to the root command tree.
	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(addAccountCmd)
	rootCmd.AddCommand(getMetadataCmd)
	rootCmd.AddCommand(checkForDuplicatesCmd)
	rootCmd.AddCommand(removeDuplicatesCmd)
	rootCmd.AddCommand(removeDuplicatesUnsafeCmd)
	rootCmd.AddCommand(shareWithMainCmd)
	rootCmd.AddCommand(syncProvidersCmd)
	rootCmd.AddCommand(balanceStorageCmd)
	rootCmd.AddCommand(freeMainCmd)
	rootCmd.AddCommand(checkTokensCmd)
}
