package cmd

import (
	"fmt"
	"os"

	"github.com/FranLegon/cloud-drives-sync/internal/config"
	"github.com/FranLegon/cloud-drives-sync/internal/database"
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/FranLegon/cloud-drives-sync/internal/model"
	"github.com/FranLegon/cloud-drives-sync/internal/task"
	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
)

var (
	safeMode       bool
	passwordFlag   string
	cfg            *model.Config
	db             *database.DB
	masterPassword string
	initialDBHash  string
	sharedRunner   *task.Runner
)

// rootCmd represents the base command
var rootCmd = &cobra.Command{
	Use:   "cloud-drives-sync",
	Short: "Synchronize files across Google Drive, Microsoft OneDrive, and Telegram",
	Long: `cloud-drives-sync is a command-line tool for managing and synchronizing files
across multiple cloud storage providers including Google Drive, Microsoft OneDrive for Business,
and Telegram.`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Gate commands based on build type
		if AutoBuild {
			if cmd.Annotations["autoBuildAllowed"] != "true" && cmd.Name() != "help" && cmd.Name() != "__complete" && cmd.Name() != "__completeNoDesc" {
				return fmt.Errorf("command %q is not available in auto builds (only auto, sync, and help are available)", cmd.Name())
			}
		}

		// Skip setup for init, help, and auto commands
		if cmd.Annotations["skipSetup"] == "true" || cmd.Name() == "help" || cmd.Name() == "__complete" || cmd.Name() == "__completeNoDesc" {
			return nil
		}

		// Get master password
		if passwordFlag != "" {
			masterPassword = passwordFlag
		} else {
			// Prompt for master password
			prompt := promptui.Prompt{
				Label: "Master Password",
				Mask:  '*',
			}

			password, err := prompt.Run()
			if err != nil {
				return fmt.Errorf("failed to read password: %w", err)
			}
			masterPassword = password
		}

		// Load configuration
		var err error
		cfg, err = config.LoadConfig(masterPassword)
		if err != nil {
			if err == config.ErrConfigNotFound {
				return fmt.Errorf("configuration not found - run 'init' command first")
			}
			if err == config.ErrInvalidPassword {
				return fmt.Errorf("invalid master password")
			}
			return fmt.Errorf("failed to load configuration: %w", err)
		}

		// Skip DB/Metadata setup for commands that manage their own lifecycle or don't need it
		if cmd.Annotations["skipDB"] == "true" {
			return nil
		}

		// Sync metadata (Download if missing)
		if err := task.DownloadMetadataDB(cfg, database.GetDBPath()); err != nil {
			return fmt.Errorf("failed to sync metadata: %w", err)
		}

		// Open database
		db, err = database.Open(masterPassword)
		if err != nil {
			return fmt.Errorf("failed to open database: %w", err)
		}

		// Initialize database schema
		if err := db.Initialize(); err != nil {
			return fmt.Errorf("failed to initialize database: %w", err)
		}

		if hash, err := db.GetMetadataHash(); err == nil {
			initialDBHash = hash
		}

		sharedRunner = task.NewRunner(cfg, db, safeMode)

		if cmd.Annotations["skipPreFlight"] != "true" {
			logger.Info("Running pre-flight checks...")
			if err := sharedRunner.RunPreFlightChecks(); err != nil {
				return err
			}
		}

		return nil
	},
	PersistentPostRunE: func(cmd *cobra.Command, args []string) error {
		// Check if DB had changes before closing
		dbHasChanges := true // Default to true if hash check fails
		if db != nil {
			if finalHash, err := db.GetMetadataHash(); err == nil && initialDBHash != "" {
				dbHasChanges = finalHash != initialDBHash
			}
			db.Close()
		}

		// Upload metadata.db if the command alters it
		if cmd.Annotations["writesDB"] == "true" && cfg != nil {
			if !dbHasChanges {
				logger.Info("No metadata changes detected, skipping upload.")
				return nil
			}
			if err := task.UploadMetadataDB(cfg, database.GetDBPath()); err != nil {
				return fmt.Errorf("failed to upload metadata.db: %w", err)
			}
			logger.Info("Metadata upload complete.")
		}

		return nil
	},
}

// Execute runs the root command
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		logger.Error("Command failed: %v", err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().BoolVarP(&safeMode, "safe", "s", false, "Dry run mode - no actual changes will be made")
	rootCmd.PersistentFlags().StringVarP(&passwordFlag, "password", "p", "", "Master password (non-interactive)")
}
