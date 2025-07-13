package cmd

import (
	"bufio"
	"cloud-drives-sync/config"
	"cloud-drives-sync/google"
	"cloud-drives-sync/microsoft"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var addAccountCmd = &cobra.Command{
	Use:   "add-account",
	Short: "Add a backup Google or Microsoft account",
	Run: func(cmd *cobra.Command, args []string) {
		exeDir, _ := os.Executable()
		exeDir = filepath.Dir(exeDir)
		configPath := filepath.Join(exeDir, "config.json.enc")
		cfg, pw, err := config.LoadConfigWithPassword(configPath)
		if err != nil {
			fmt.Printf("Failed to load config: %v\n", err)
			os.Exit(1)
		}
		reader := bufio.NewReader(os.Stdin)
		fmt.Print("Add account for which provider? (Google/Microsoft): ")
		prov, _ := reader.ReadString('\n')
		prov = strings.TrimSpace(prov)
		if prov != "Google" && prov != "Microsoft" {
			fmt.Println("Invalid provider.")
			os.Exit(1)
		}
		if !cfg.HasMainAccount(prov) {
			fmt.Printf("You must add a main %s account first (run 'init').\n", prov)
			os.Exit(1)
		}
		if prov == "Google" {
			google.AddBackupAccount(&cfg, pw, configPath)
		} else {
			microsoft.AddBackupAccount(&cfg, pw, configPath)
		}
	},
}

func init() {
	rootCmd.AddCommand(addAccountCmd)
}
