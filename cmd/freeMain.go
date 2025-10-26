package cmd

import (
	"fmt"

	"cloud-drives-sync/internal/logger"
	"cloud-drives-sync/internal/model"
	"cloud-drives-sync/internal/task"

	"github.com/spf13/cobra"
)

var freeMainCmd = &cobra.Command{
	Use:   "free-main [provider]",
	Short: "Move all files from main account to backup accounts",
	Long: `Transfers all files from a main account's synched-cloud-drives folder to
its backup accounts. Specify 'google' or 'microsoft' as the provider.`,
	Args: cobra.ExactArgs(1),
	RunE: runFreeMain,
}

func runFreeMain(cmd *cobra.Command, args []string) error {
	ctx := getContext()
	log := logger.New().WithPrefix("FREE-MAIN")

	providerArg := args[0]
	var provider model.Provider

	if providerArg == "google" || providerArg == "g" {
		provider = model.ProviderGoogle
	} else if providerArg == "microsoft" || providerArg == "m" || providerArg == "ms" {
		provider = model.ProviderMicrosoft
	} else {
		log.Fatal("Invalid provider. Use 'google' or 'microsoft'")
	}

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

	// Update metadata
	log.Info("Updating metadata...")
	if err := runner.GetMetadata(ctx, clients); err != nil {
		log.Error("Failed to update metadata: %v", err)
	}

	// Free main account
	fmt.Printf("This will move ALL files from the main %s account to backup accounts.\n", provider)
	fmt.Print("Continue? (yes/no): ")
	var confirm string
	fmt.Scanln(&confirm)

	if confirm != "yes" && confirm != "y" {
		log.Info("Operation cancelled")
		return nil
	}

	if err := runner.FreeMainAccount(ctx, clients, provider); err != nil {
		log.Fatal("Failed to free main account: %v", err)
	}

	log.Success("Main account freed successfully")
	return nil
}
