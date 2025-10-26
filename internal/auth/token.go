package auth

import (
	"context"
	"fmt"

	"golang.org/x/oauth2"
)

// TokenSource creates an OAuth2 token source from a refresh token
func TokenSource(ctx context.Context, config *oauth2.Config, refreshToken string) oauth2.TokenSource {
	token := &oauth2.Token{
		RefreshToken: refreshToken,
	}
	return config.TokenSource(ctx, token)
}

// GetAccessToken retrieves a valid access token from a token source
func GetAccessToken(ctx context.Context, tokenSource oauth2.TokenSource) (string, error) {
	token, err := tokenSource.Token()
	if err != nil {
		return "", fmt.Errorf("failed to get access token: %w", err)
	}
	return token.AccessToken, nil
}

// ValidateToken checks if a token source can produce a valid token
func ValidateToken(ctx context.Context, tokenSource oauth2.TokenSource) error {
	_, err := tokenSource.Token()
	if err != nil {
		return fmt.Errorf("token validation failed: %w", err)
	}
	return nil
}
