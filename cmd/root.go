package cmd

import (
	"fmt"
	"io"
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
				return fmt.Errorf("command %q is not available in auto builds (only sync, config --auto, and help are available)", cmd.Name())
			}
			// The deployment (auto) sync is headless: no logs or detailed output, only exit codes.
			if cmd.Name() == "sync" {
				logger.SetOutput(io.Discard)
			}
		}

		// Commands that manage their own setup lifecycle (config dispatches per-action).
		if cmd.Annotations["skipSetup"] == "true" || cmd.Name() == "help" || cmd.Name() == "__complete" || cmd.Name() == "__completeNoDesc" {
			return nil
		}

		if err := setupConfig(); err != nil {
			return err
		}

		if cmd.Annotations["skipDB"] == "true" {
			return nil
		}

		preflight := cmd.Annotations["skipPreFlight"] != "true"
		return setupDBAndRunner(preflight)
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

// setupPassword resolves the master password from the flag or an interactive prompt.
func setupPassword() error {
	if masterPassword != "" {
		return nil
	}
	if passwordFlag != "" {
		masterPassword = passwordFlag
		return nil
	}
	prompt := promptui.Prompt{
		Label: "Master Password",
		Mask:  '*',
	}
	password, err := prompt.Run()
	if err != nil {
		return fmt.Errorf("failed to read password: %w", err)
	}
	masterPassword = password
	return nil
}

// setupConfig ensures the master password and decrypted configuration are loaded.
func setupConfig() error {
	if err := setupPassword(); err != nil {
		return err
	}
	if cfg != nil {
		return nil
	}
	var err error
	cfg, err = config.LoadConfig(masterPassword)
	if err != nil {
		if err == config.ErrConfigNotFound {
			return fmt.Errorf("configuration not found - run 'config --init' first")
		}
		if err == config.ErrInvalidPassword {
			return fmt.Errorf("invalid master password")
		}
		return fmt.Errorf("failed to load configuration: %w", err)
	}
	return nil
}

// setupDBAndRunner downloads the freshest metadata database, opens it, initializes the
// schema, builds the shared task runner and optionally runs pre-flight checks.
func setupDBAndRunner(preflight bool) error {
	if err := task.DownloadMetadataDB(cfg, database.GetDBPath()); err != nil {
		return fmt.Errorf("failed to sync metadata: %w", err)
	}

	var err error
	db, err = database.Open(masterPassword)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	if err := db.Initialize(); err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}

	if hash, err := db.GetMetadataHash(); err == nil {
		initialDBHash = hash
	}

	sharedRunner = task.NewRunner(cfg, db, safeMode)

	if preflight {
		logger.Info("Running pre-flight checks...")
		if err := sharedRunner.RunPreFlightChecks(); err != nil {
			return err
		}
	}

	return nil
}

func init() {
	rootCmd.CompletionOptions.DisableDefaultCmd = true
	rootCmd.PersistentFlags().StringVarP(&passwordFlag, "password", "p", "", "Master password (non-interactive)")
}
