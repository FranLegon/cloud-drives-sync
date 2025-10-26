package auth

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"cloud-drives-sync/internal/logger"
	"cloud-drives-sync/internal/model"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"golang.org/x/oauth2/microsoft"
)

const (
	// OAuth redirect URI for local callback server
	RedirectURL = "http://localhost:8080/callback"

	// Google OAuth scopes
	GoogleDriveScope = "https://www.googleapis.com/auth/drive"
	GoogleEmailScope = "https://www.googleapis.com/auth/userinfo.email"

	// Microsoft OAuth scopes
	MicrosoftFilesScope   = "files.readwrite.all"
	MicrosoftUserScope    = "user.read"
	MicrosoftOfflineScope = "offline_access"
)

// OAuthConfig creates an OAuth2 configuration for a provider
func OAuthConfig(provider model.Provider, clientID, clientSecret string) (*oauth2.Config, error) {
	switch provider {
	case model.ProviderGoogle:
		return &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  RedirectURL,
			Scopes: []string{
				GoogleDriveScope,
				GoogleEmailScope,
			},
			Endpoint: google.Endpoint,
		}, nil

	case model.ProviderMicrosoft:
		return &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  RedirectURL,
			Scopes: []string{
				MicrosoftFilesScope,
				MicrosoftUserScope,
				MicrosoftOfflineScope,
			},
			Endpoint: microsoft.AzureADEndpoint("common"), // Supports both personal and organizational accounts
		}, nil

	default:
		return nil, fmt.Errorf("unsupported provider: %s", provider)
	}
}

// PerformOAuthFlow initiates the OAuth flow and returns the refresh token
func PerformOAuthFlow(ctx context.Context, config *oauth2.Config, log *logger.Logger) (string, error) {
	// Generate a random state for CSRF protection
	state := generateRandomState()

	// Get the authorization URL
	authURL := config.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.ApprovalForce)

	log.Info("Please visit this URL to authorize the application:")
	log.Info("%s", authURL)

	// Channel to receive the authorization code
	codeChan := make(chan string, 1)
	errChan := make(chan error, 1)

	// Start local web server to handle OAuth callback
	server := &http.Server{Addr: ":8080"}

	http.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		// Verify state parameter
		if r.URL.Query().Get("state") != state {
			errChan <- fmt.Errorf("state mismatch")
			fmt.Fprintf(w, "Error: State mismatch. You can close this window.")
			return
		}

		// Get authorization code
		code := r.URL.Query().Get("code")
		if code == "" {
			errChan <- fmt.Errorf("no authorization code received")
			fmt.Fprintf(w, "Error: No authorization code received. You can close this window.")
			return
		}

		codeChan <- code
		fmt.Fprintf(w, "Authorization successful! You can close this window and return to the terminal.")
	})

	// Start server in a goroutine
	go func() {
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			errChan <- fmt.Errorf("server error: %w", err)
		}
	}()

	// Wait for code or error with timeout
	var code string
	select {
	case code = <-codeChan:
		// Success
	case err := <-errChan:
		server.Shutdown(ctx)
		return "", err
	case <-time.After(5 * time.Minute):
		server.Shutdown(ctx)
		return "", fmt.Errorf("OAuth flow timed out after 5 minutes")
	}

	// Shutdown the server
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server.Shutdown(shutdownCtx)

	// Exchange authorization code for token
	token, err := config.Exchange(ctx, code)
	if err != nil {
		return "", fmt.Errorf("failed to exchange code for token: %w", err)
	}

	if token.RefreshToken == "" {
		return "", fmt.Errorf("no refresh token received (user may have already authorized)")
	}

	return token.RefreshToken, nil
}

// generateRandomState creates a random state string for OAuth CSRF protection
func generateRandomState() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
