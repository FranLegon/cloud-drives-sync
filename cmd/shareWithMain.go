package cmd

import (
	"cloud-drives-sync/internal/logger"
	"cloud-drives-sync/internal/task"

	"github.com/spf13/cobra"
)

var shareWithMainCmd = &cobra.Command{
	Use:   "share-with-main",
	Short: "Ensure backup accounts have access to sync folders",
	Long: `Verifies that all backup accounts have editor access to their provider's
main account sync folder. Re-applies permissions if missing.`,
	RunE: runShareWithMain,
}

func runShareWithMain(cmd *cobra.Command, args []string) error {
	ctx := getContext()
	log := logger.New().WithPrefix("SHARE-WITH-MAIN")

	// Load config and database
	cfg, password, err := loadConfig()
	if err != nil {
		log.Fatal("Failed to load config: %v", err)
	}

	db, err := openDatabase(password)
	if err != nil {
		log.Fatal("Failed to open database: %v", err)
	}
	defer db.Close()

	// Create clients
	clients, err := createAllClients(ctx, cfg)
	if err != nil {
		log.Fatal("Failed to create clients: %v", err)
	}

	// Create task runner
	runner := task.NewTaskRunner(cfg, db, password, safeMode)

	// Run pre-flight checks
	if err := runner.PreflightCheck(ctx, clients); err != nil {
		log.Fatal("Pre-flight check failed: %v", err)
	}

	// Share with main
	if err := runner.ShareWithMain(ctx, clients); err != nil {
		log.Fatal("Failed to share with main: %v", err)
	}

	log.Success("Sharing complete")
	return nil
}
