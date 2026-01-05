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
)

// rootCmd represents the base command
var rootCmd = &cobra.Command{
	Use:   "cloud-drives-sync",
	Short: "Synchronize files across Google Drive, Microsoft OneDrive, and Telegram",
	Long: `cloud-drives-sync is a command-line tool for managing and synchronizing files
across multiple cloud storage providers including Google Drive, Microsoft OneDrive for Business,
and Telegram.`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Skip setup for init and help commands
		if cmd.Name() == "init" || cmd.Name() == "help" || cmd.Name() == "__complete" || cmd.Name() == "__completeNoDesc" {
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

		// Open database
		db, err = database.Open(masterPassword)
		if err != nil {
			return fmt.Errorf("failed to open database: %w", err)
		}

		// Initialize database schema
		if err := db.Initialize(); err != nil {
			return fmt.Errorf("failed to initialize database: %w", err)
		}

		return nil
	},
	PersistentPostRunE: func(cmd *cobra.Command, args []string) error {
		if db != nil {
			return db.Close()
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

// getTaskRunner creates a task runner with current config and db
func getTaskRunner() *task.Runner {
	return task.NewRunner(cfg, db, safeMode)
}

// requiresPreFlightCheck runs pre-flight checks for commands that need them
func requiresPreFlightCheck(runner *task.Runner) error {
	logger.Info("Running pre-flight checks...")
	return runner.RunPreFlightChecks()
}
