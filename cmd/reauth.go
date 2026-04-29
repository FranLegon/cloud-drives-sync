package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/FranLegon/cloud-drives-sync/internal/auth"
	"github.com/FranLegon/cloud-drives-sync/internal/config"
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/FranLegon/cloud-drives-sync/internal/model"
	"github.com/FranLegon/cloud-drives-sync/internal/telegram"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
)

var reauthAll bool

var reauthCmd = &cobra.Command{
	Use:   "reauth",
	Short: "Re-authenticate cloud provider accounts",
	Long: `Re-authenticates accounts whose tokens have expired or become invalid.
By default only broken connections are re-authenticated.
Use --all to force re-authentication of every account.`,
	RunE: runReauth,
}

func init() {
	reauthCmd.Flags().BoolVarP(&reauthAll, "all", "a", false, "Re-authenticate all accounts, not just broken ones")
	rootCmd.AddCommand(reauthCmd)
}

func runReauth(cmd *cobra.Command, args []string) error {
	var usersToReauth []*model.User

	for i := range cfg.Users {
		user := &cfg.Users[i]

		if reauthAll {
			usersToReauth = append(usersToReauth, user)
			continue
		}

		// Check if token is broken
		if isBroken, reason := isTokenBroken(user); isBroken {
			logger.WarningTagged([]string{string(user.Provider), userIdentifier(user)}, "Token invalid: %s", reason)
			usersToReauth = append(usersToReauth, user)
		} else {
			logger.InfoTagged([]string{string(user.Provider), userIdentifier(user)}, "Token is valid, skipping")
		}
	}

	if len(usersToReauth) == 0 {
		logger.Info("All tokens are valid — nothing to re-authenticate")
		return nil
	}

	logger.Info("Re-authenticating %d account(s)...", len(usersToReauth))

	var failedAccounts []string
	for _, user := range usersToReauth {
		if err := reauthUser(user); err != nil {
			logger.ErrorTagged([]string{string(user.Provider), userIdentifier(user)}, "Re-authentication failed: %v", err)
			failedAccounts = append(failedAccounts, userIdentifier(user))
			continue
		}
		logger.InfoTagged([]string{string(user.Provider), userIdentifier(user)}, "Re-authenticated successfully")
	}

	// Save updated config
	if err := config.SaveConfig(cfg, masterPassword); err != nil {
		return fmt.Errorf("failed to save configuration: %w", err)
	}

	if len(failedAccounts) > 0 {
		return fmt.Errorf("re-authentication failed for: %v", failedAccounts)
	}

	logger.Info("All accounts re-authenticated successfully")
	return nil
}

func isTokenBroken(user *model.User) (bool, string) {
	switch user.Provider {
	case model.ProviderGoogle:
		oauthCfg := auth.GetGoogleOAuthConfig(cfg.GoogleClient.ID, cfg.GoogleClient.Secret)
		if err := auth.ValidateToken(oauthCfg, user.RefreshToken); err != nil {
			return true, err.Error()
		}
	case model.ProviderMicrosoft:
		oauthCfg := auth.GetMicrosoftOAuthConfig(cfg.MicrosoftClient.ID, cfg.MicrosoftClient.Secret)
		if err := auth.ValidateToken(oauthCfg, user.RefreshToken); err != nil {
			return true, err.Error()
		}
	case model.ProviderTelegram:
		client, err := telegram.NewClient(user, cfg.TelegramClient.APIID, cfg.TelegramClient.APIHash)
		if err != nil {
			return true, err.Error()
		}
		defer client.Close()
		if err := client.PreFlightCheck(); err != nil {
			return true, err.Error()
		}
	}
	return false, ""
}

func reauthUser(user *model.User) error {
	switch user.Provider {
	case model.ProviderGoogle:
		return reauthOAuth(user, auth.GetGoogleOAuthConfig(cfg.GoogleClient.ID, cfg.GoogleClient.Secret), auth.GetGoogleUserEmail)
	case model.ProviderMicrosoft:
		return reauthOAuth(user, auth.GetMicrosoftOAuthConfig(cfg.MicrosoftClient.ID, cfg.MicrosoftClient.Secret), auth.GetMicrosoftUserEmail)
	case model.ProviderTelegram:
		return reauthTelegram(user)
	default:
		return fmt.Errorf("unsupported provider: %s", user.Provider)
	}
}

func reauthOAuth(user *model.User, oauthConfig *oauth2.Config, getEmail func(context.Context, *oauth2.Token, *oauth2.Config) (string, error)) error {
	ctx := context.Background()

	server := auth.NewOAuthServer()
	if err := server.Start(); err != nil {
		return fmt.Errorf("failed to start OAuth server: %w", err)
	}
	defer server.Stop()

	state := auth.GenerateStateToken()
	authURL := oauthConfig.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"))

	logger.Info("Please visit the following URL to authorize %s:", userIdentifier(user))
	fmt.Println(authURL)

	code, err := server.WaitForCode(state, 120*time.Second)
	if err != nil {
		return fmt.Errorf("authorization failed: %w", err)
	}

	token, err := auth.ExchangeCode(ctx, oauthConfig, code)
	if err != nil {
		return fmt.Errorf("failed to exchange code: %w", err)
	}

	// Verify the email matches the existing account
	email, err := getEmail(ctx, token, oauthConfig)
	if err != nil {
		return fmt.Errorf("failed to get user email: %w", err)
	}

	if email != user.Email {
		return fmt.Errorf("email mismatch: expected %s but got %s — please authorize the correct account", user.Email, email)
	}

	if token.RefreshToken == "" {
		return fmt.Errorf("no refresh token received — try revoking app access and retrying")
	}

	user.RefreshToken = token.RefreshToken
	logger.Info("Refresh token updated for %s", user.Email)
	return nil
}

func reauthTelegram(user *model.User) error {
	client, err := telegram.NewClient(user, cfg.TelegramClient.APIID, cfg.TelegramClient.APIHash)
	if err != nil {
		return fmt.Errorf("failed to create telegram client: %w", err)
	}
	defer client.Close()

	logger.Info("Starting Telegram re-authentication for %s...", user.Phone)
	if err := client.Authenticate(user.Phone); err != nil {
		return fmt.Errorf("authentication failed: %w", err)
	}

	return nil
}

func userIdentifier(user *model.User) string {
	if user.Email != "" {
		return user.Email
	}
	return user.Phone
}
