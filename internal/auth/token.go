package auth

import (
	"context"
	"fmt"

	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/config"
	"golang.org/x/oauth2"
)

// NewTokenSource creates a reusable oauth2.TokenSource for a specific user.
// This source uses the user's stored refresh token to automatically acquire new
// access tokens as needed, handling expiration transparently.
func NewTokenSource(ctx context.Context, user *config.User, appCfg *config.AppConfig) (oauth2.TokenSource, error) {
	var oauthCfg *oauth2.Config
	switch user.Provider {
	case "Google":
		oauthCfg = GetGoogleOAuthConfig(appCfg)
	case "Microsoft":
		oauthCfg = GetMicrosoftOAuthConfig(appCfg)
	default:
		return nil, fmt.Errorf("unknown provider for token source: %s", user.Provider)
	}

	if user.RefreshToken == "" {
		return nil, fmt.Errorf("user %s has no refresh token", user.Email)
	}

	// Create a token struct with the essential refresh token.
	token := &oauth2.Token{
		RefreshToken: user.RefreshToken,
	}

	// The returned TokenSource will use the refresh token to get new access tokens.
	return oauthCfg.TokenSource(ctx, token), nil
}
