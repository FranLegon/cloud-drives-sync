package cmd

import (
	"bufio"
	"cloud-drives-sync/config"
	"cloud-drives-sync/database"
	"cloud-drives-sync/google"
	"cloud-drives-sync/microsoft"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize configuration and database",
	Run: func(cmd *cobra.Command, args []string) {
		exeDir, _ := os.Executable()
		exeDir = filepath.Dir(exeDir)
		configPath := filepath.Join(exeDir, "config.json.enc")
		dbPath := filepath.Join(exeDir, "metadata.db")

		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			fmt.Println("No encrypted config found. Starting first-time setup.")
			reader := bufio.NewReader(os.Stdin)
			fmt.Print("Create a master password: ")
			pw, _ := reader.ReadString('\n')
			pw = strings.TrimSpace(pw)
			fmt.Print("Confirm password: ")
			pw2, _ := reader.ReadString('\n')
			pw2 = strings.TrimSpace(pw2)
			if pw != pw2 {
				fmt.Println("Passwords do not match.")
				os.Exit(1)
			}
			fmt.Print("Enter Google Client ID: ")
			gid, _ := reader.ReadString('\n')
			fmt.Print("Enter Google Client Secret: ")
			gsec, _ := reader.ReadString('\n')
			fmt.Print("Enter Microsoft Client ID: ")
			mid, _ := reader.ReadString('\n')
			fmt.Print("Enter Microsoft Client Secret: ")
			msec, _ := reader.ReadString('\n')

			cfg := config.Config{
				GoogleClient: config.ClientCreds{
					ID:     strings.TrimSpace(gid),
					Secret: strings.TrimSpace(gsec),
				},
				MicrosoftClient: config.ClientCreds{
					ID:     strings.TrimSpace(mid),
					Secret: strings.TrimSpace(msec),
				},
				Users: []config.User{},
			}
			if err := config.EncryptAndSaveConfig(cfg, configPath, pw); err != nil {
				fmt.Printf("Failed to save config: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("Config encrypted and saved.")
		}

		if _, err := os.Stat(dbPath); os.IsNotExist(err) {
			fmt.Println("Creating metadata.db...")
			if err := database.InitDB(dbPath); err != nil {
				fmt.Printf("Failed to create DB: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("Database initialized.")
		}

		cfg, pw, err := config.LoadConfigWithPassword(configPath)
		if err != nil {
			fmt.Printf("Failed to load config: %v\n", err)
			os.Exit(1)
		}
		reader := bufio.NewReader(os.Stdin)
		for _, provider := range []string{"Google", "Microsoft"} {
			if !cfg.HasMainAccount(provider) {
				fmt.Printf("Add main account for %s now? (y/n): ", provider)
				resp, _ := reader.ReadString('\n')
				if strings.ToLower(strings.TrimSpace(resp)) == "y" {
					if provider == "Google" {
						google.AddMainAccount(&cfg, pw, configPath)
					} else {
						microsoft.AddMainAccount(&cfg, pw, configPath)
					}
				}
			}
		}

		for _, u := range cfg.Users {
			if u.IsMain {
				switch u.Provider {
				case "Google":
					google.EnsureSyncFolder(u, cfg.GoogleClient, pw)
				case "Microsoft":
					microsoft.EnsureSyncFolder(u, cfg.MicrosoftClient, pw)
				}
			}
		}
		fmt.Println("Initialization complete.")
	},
}

func init() {
	rootCmd.AddCommand(initCmd)
}
