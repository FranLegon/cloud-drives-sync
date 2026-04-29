package cmd

import (
	"fmt"

	"github.com/FranLegon/cloud-drives-sync/internal/config"
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/FranLegon/cloud-drives-sync/internal/model"
	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
)

var removeAccountCmd = &cobra.Command{
	Use:   "remove-account",
	Short: "Remove a backup account or an entire provider from the configuration",
	Long: `Removes a backup account or all accounts for a provider from the configuration.
Main accounts can only be removed if no backup accounts exist for that provider.
This does NOT delete files from the cloud provider.`,
	RunE: runRemoveAccount,
}

func init() {
	rootCmd.AddCommand(removeAccountCmd)
}

func runRemoveAccount(cmd *cobra.Command, args []string) error {
	// Build list of accounts for selection
	type accountOption struct {
		Label    string
		Provider model.Provider
		Email    string
		Phone    string
		IsMain   bool
	}

	var options []accountOption
	var labels []string

	for _, user := range cfg.Users {
		var label string
		identifier := user.Email
		if user.Provider == model.ProviderTelegram {
			identifier = user.Phone
		}
		role := "backup"
		if user.IsMain {
			role = "main"
		}
		label = fmt.Sprintf("[%s] %s (%s)", user.Provider, identifier, role)
		options = append(options, accountOption{
			Label:    label,
			Provider: user.Provider,
			Email:    user.Email,
			Phone:    user.Phone,
			IsMain:   user.IsMain,
		})
		labels = append(labels, label)
	}

	if len(options) == 0 {
		return fmt.Errorf("no accounts configured")
	}

	// Prompt user to select account
	selectPrompt := promptui.Select{
		Label: "Select account to remove",
		Items: labels,
	}
	idx, _, err := selectPrompt.Run()
	if err != nil {
		return fmt.Errorf("selection cancelled: %w", err)
	}

	selected := options[idx]

	// If removing a main account, check that no backup accounts exist for that provider
	if selected.IsMain {
		backups := config.GetBackupAccounts(cfg, selected.Provider)
		if len(backups) > 0 {
			return fmt.Errorf("cannot remove main account for %s: %d backup account(s) still exist - remove them first", selected.Provider, len(backups))
		}
	}

	// Confirm removal
	confirmPrompt := promptui.Prompt{
		Label:     fmt.Sprintf("Remove %s", selected.Label),
		IsConfirm: true,
	}
	_, err = confirmPrompt.Run()
	if err != nil {
		logger.Info("Removal cancelled")
		return nil
	}

	// Remove the account from config
	var newUsers []model.User
	for _, u := range cfg.Users {
		if u.Provider == selected.Provider {
			if selected.Provider == model.ProviderTelegram {
				if u.Phone == selected.Phone {
					continue
				}
			} else {
				if u.Email == selected.Email {
					continue
				}
			}
		}
		newUsers = append(newUsers, u)
	}
	cfg.Users = newUsers

	// Save updated configuration
	if err := config.SaveConfig(cfg, masterPassword); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	logger.Info("Account removed successfully: %s", selected.Label)
	return nil
}
