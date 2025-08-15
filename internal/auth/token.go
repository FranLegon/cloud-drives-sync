package auth

import (
	"cloud-drives-sync/internal/config"
	"context"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"golang.org/x/oauth2/microsoft"
)

// GetGoogleTokenSource creates a Google OAuth2 token source from a stored refresh token.
// The token source will automatically handle refreshing the access token when it expires.
func GetGoogleTokenSource(ctx context.Context, cfg *config.Config, refreshToken string) (oauth2.TokenSource, error) {
	oauthCfg := &oauth2.Config{
		ClientID:     cfg.GoogleClient.ID,
		ClientSecret: cfg.GoogleClient.Secret,
		Endpoint:     google.Endpoint,
		Scopes: []string{
			"https://www.googleapis.com/auth/drive",
			"https://www.googleapis.com/auth/userinfo.email",
		},
	}

	token := &oauth2.Token{
		RefreshToken: refreshToken,
		// Access token will be fetched on the first use.
	}

	return oauthCfg.TokenSource(ctx, token), nil
}

// GetMicrosoftTokenSource creates a Microsoft OAuth2 token source from a stored refresh token.
// It is used by the adapter that provides tokens to the Microsoft Graph SDK.
func GetMicrosoftTokenSource(ctx context.Context, cfg *config.Config, refreshToken string) (oauth2.TokenSource, error) {
	oauthCfg := &oauth2.Config{
		ClientID:     cfg.MicrosoftClient.ID,
		ClientSecret: cfg.MicrosoftClient.Secret,
		Endpoint:     microsoft.LiveConnectEndpoint,
		Scopes: []string{
			"files.readwrite.all",
			"user.read",
			"offline_access",
		},
	}

	token := &oauth2.Token{
		RefreshToken: refreshToken,
	}

	return oauthCfg.TokenSource(ctx, token), nil
}
