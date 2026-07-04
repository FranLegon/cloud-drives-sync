//go:build !auto

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	cfgInit          bool
	cfgAddAccount    bool
	cfgRemoveAccount bool
	cfgCheckTokens   bool
	cfgReauth        bool
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage configuration and accounts",
	Long: `Manage the tool's configuration and accounts.

Exactly one action flag must be provided:
  --init             First-time setup (or update credentials/main account)
  --add-account      Authorize and register a backup account
  --remove-account   Remove an account from the local configuration
  --check-tokens     Report which stored credentials still work
  --reauth           Re-authenticate broken credentials (--all for every account)`,
	Annotations: map[string]string{
		// config dispatches per-action setup itself.
		"skipSetup": "true",
	},
	RunE: runConfig,
}

func init() {
	configCmd.Flags().BoolVar(&cfgInit, "init", false, "First-time setup or update credentials/main account")
	configCmd.Flags().BoolVar(&cfgAddAccount, "add-account", false, "Authorize and register a backup account")
	configCmd.Flags().BoolVar(&cfgRemoveAccount, "remove-account", false, "Remove an account from the local configuration")
	configCmd.Flags().BoolVar(&cfgCheckTokens, "check-tokens", false, "Report which stored credentials still work")
	configCmd.Flags().BoolVar(&cfgReauth, "reauth", false, "Re-authenticate broken credentials")

	// Sub-flags shared with the individual action handlers.
	configCmd.Flags().BoolVarP(&reauthAll, "all", "a", false, "With --reauth: re-authenticate every account, not just broken ones")
	configCmd.Flags().StringVarP(&jsonFlag, "json", "j", "", "With --init: JSON string containing client credentials")
	configCmd.Flags().BoolVarP(&getJsonFlag, "getjson", "g", false, "With --init: output configuration as JSON string")

	rootCmd.AddCommand(configCmd)
}

func runConfig(cmd *cobra.Command, args []string) error {
	actions := []bool{cfgInit, cfgAddAccount, cfgRemoveAccount, cfgCheckTokens, cfgReauth}
	count := 0
	for _, a := range actions {
		if a {
			count++
		}
	}
	if count == 0 {
		return fmt.Errorf("config requires exactly one action flag (--init, --add-account, --remove-account, --check-tokens, or --reauth)")
	}
	if count > 1 {
		return fmt.Errorf("config action flags are mutually exclusive; provide exactly one")
	}

	switch {
	case cfgInit:
		// init manages its own password/config/database lifecycle.
		return runInit(cmd, args)
	case cfgAddAccount:
		if err := setupConfig(); err != nil {
			return err
		}
		return runAddAccount(cmd, args)
	case cfgRemoveAccount:
		if err := setupConfig(); err != nil {
			return err
		}
		return runRemoveAccount(cmd, args)
	case cfgCheckTokens:
		if err := setupConfig(); err != nil {
			return err
		}
		if err := setupDBAndRunner(false); err != nil {
			return err
		}
		return runCheckTokens(cmd, args)
	case cfgReauth:
		if err := setupConfig(); err != nil {
			return err
		}
		return runReauth(cmd, args)
	}
	return nil
}
