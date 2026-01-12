package auth

import (
	"context"
	"fmt"
	"sync"

	"golang.org/x/oauth2"
)

// TokenSource wraps an oauth2.TokenSource with automatic refresh
type TokenSource struct {
	config       *oauth2.Config
	refreshToken string
	currentToken *oauth2.Token
	mu           sync.Mutex
}

// NewTokenSource creates a new TokenSource
func NewTokenSource(config *oauth2.Config, refreshToken string) *TokenSource {
	return &TokenSource{
		config:       config,
		refreshToken: refreshToken,
	}
}

// Token returns a valid token, refreshing if necessary
func (ts *TokenSource) Token() (*oauth2.Token, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	if ts.currentToken != nil && ts.currentToken.Valid() {
		return ts.currentToken, nil
	}

	// Token needs refresh
	token := &oauth2.Token{
		RefreshToken: ts.refreshToken,
	}

	ctx := context.Background()
	newToken, err := ts.config.TokenSource(ctx, token).Token()
	if err != nil {
		return nil, fmt.Errorf("failed to refresh token: %w", err)
	}

	ts.currentToken = newToken
	return newToken, nil
}

// GetRefreshToken returns the current refresh token
func (ts *TokenSource) GetRefreshToken() string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.currentToken != nil && ts.currentToken.RefreshToken != "" {
		return ts.currentToken.RefreshToken
	}
	return ts.refreshToken
}

// ValidateToken checks if a token can be refreshed
func ValidateToken(config *oauth2.Config, refreshToken string) error {
	ts := NewTokenSource(config, refreshToken)
	_, err := ts.Token()
	return err
}
