package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/FranLegon/cloud-drives-sync/internal/api"
	"github.com/FranLegon/cloud-drives-sync/internal/auth"
	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"github.com/FranLegon/cloud-drives-sync/internal/model"
	"github.com/FranLegon/cloud-drives-sync/internal/task"
	"golang.org/x/oauth2"
)

func getClientForFile(runner *task.Runner, file *model.File) (api.CloudClient, error) {
	// Use the first replica if file has replicas
	if len(file.Replicas) > 0 {
		return getClientForReplica(runner, file.Replicas[0])
	}

	// If no replicas, we can't determine which client to use
	return nil, fmt.Errorf("file has no replicas, cannot determine client")
}

func getClientForReplica(runner *task.Runner, replica *model.Replica) (api.CloudClient, error) {
	// Get client for the replica's provider and account
	var email, phone string
	if replica.Provider == model.ProviderTelegram {
		phone = replica.AccountID
	} else {
		email = replica.AccountID
	}

	return runner.GetOrCreateClient(&model.User{
		Provider:     replica.Provider,
		Email:        email,
		Phone:        phone,
		RefreshToken: "", // Will use from config
	})
}

// performOAuthFlow runs the full OAuth server+redirect+exchange flow for Google or Microsoft.
// It returns the token and the authenticated email address.
func performOAuthFlow(provider string, c *model.Config) (*oauth2.Token, string, error) {
	var oauthConfig *oauth2.Config
	var getEmail func(context.Context, *oauth2.Token, *oauth2.Config) (string, error)

	switch provider {
	case "Google":
		oauthConfig = auth.GetGoogleOAuthConfig(c.GoogleClient.ID, c.GoogleClient.Secret)
		getEmail = auth.GetGoogleUserEmail
	case "Microsoft":
		oauthConfig = auth.GetMicrosoftOAuthConfig(c.MicrosoftClient.ID, c.MicrosoftClient.Secret)
		getEmail = auth.GetMicrosoftUserEmail
	default:
		return nil, "", fmt.Errorf("unsupported provider: %s", provider)
	}

	server := auth.NewOAuthServer()
	if err := server.Start(); err != nil {
		return nil, "", fmt.Errorf("failed to start OAuth server: %w", err)
	}
	defer server.Stop()

	state := auth.GenerateStateToken()
	authURL := oauthConfig.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"))

	logger.Info("Please visit the following URL to authorize:")
	fmt.Println(authURL)

	code, err := server.WaitForCode(state, 120*time.Second)
	if err != nil {
		return nil, "", fmt.Errorf("authorization failed: %w", err)
	}

	ctx := context.Background()
	token, err := auth.ExchangeCode(ctx, oauthConfig, code)
	if err != nil {
		return nil, "", fmt.Errorf("failed to exchange code: %w", err)
	}

	email, err := getEmail(ctx, token, oauthConfig)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get user email: %w", err)
	}

	return token, email, nil
}
