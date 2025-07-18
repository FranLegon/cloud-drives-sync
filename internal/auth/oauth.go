package auth

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/config"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/logger"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	// redirectURL is the callback endpoint for the OAuth 2.0 flow. It must be
	// registered in the Google Cloud and Azure application settings.
	redirectURL = "http://localhost:8080/oauth/callback"
)

// GetGoogleOAuthConfig builds the OAuth 2.0 configuration for Google Drive. [3, 4]
func GetGoogleOAuthConfig(cfg *config.AppConfig) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     cfg.GoogleClient.ID,
		ClientSecret: cfg.GoogleClient.Secret,
		RedirectURL:  redirectURL,
		// Scopes are requested as per the requirements document.
		Scopes: []string{
			"https://www.googleapis.com/auth/drive",
			"https://www.googleapis.com/auth/userinfo.email",
		},
		Endpoint: google.Endpoint,
	}
}

// GetMicrosoftOAuthConfig builds the OAuth 2.0 configuration for Microsoft Graph.
func GetMicrosoftOAuthConfig(cfg *config.AppConfig) *oauth2.Config {
	// The 'common' endpoint supports both personal and organizational accounts.
	return &oauth2.Config{
		ClientID:     cfg.MicrosoftClient.ID,
		ClientSecret: cfg.MicrosoftClient.Secret,
		RedirectURL:  redirectURL,
		// Scopes are requested as per the requirements document.
		Scopes: []string{
			"files.readwrite.all",
			"user.read",
			"offline_access", // This scope is required to get a refresh token.
		},
		Endpoint: oauth2.Endpoint{
			AuthURL:  "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
			TokenURL: "https://login.microsoftonline.com/common/oauth2/v2.0/token",
		},
	}
}

// GetTokenFromWeb orchestrates the three-legged OAuth 2.0 flow by starting a
// local web server to capture the authorization code from the provider's redirect.
func GetTokenFromWeb(ctx context.Context, conf *oauth2.Config) (*oauth2.Token, error) {
	// Generate the authorization URL that the user must visit.
	// AccessTypeOffline is crucial for obtaining a refresh token.
	authURL := conf.AuthCodeURL("state-token", oauth2.AccessTypeOffline)

	logger.Info("A browser window should open. If not, please manually open this URL:")
	logger.Info(authURL)

	// Channel to receive the authorization code from the HTTP handler.
	codeChan := make(chan string)
	errChan := make(chan error)

	// Configure the local server.
	mux := http.NewServeMux()
	server := &http.Server{
		Addr:    ":8080",
		Handler: mux,
	}

	// The handler for the callback URL.
	mux.HandleFunc("/oauth/callback", func(w http.ResponseWriter, r *http.Request) {
		// Check for an error from the provider.
		if errMsg := r.URL.Query().Get("error"); errMsg != "" {
			errDesc := r.URL.Query().Get("error_description")
			http.Error(w, fmt.Sprintf("OAuth Error: %s - %s", errMsg, errDesc), http.StatusBadRequest)
			errChan <- fmt.Errorf("authentication failed: %s", errDesc)
			return
		}

		// Extract the authorization code.
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "Error: Did not receive authorization code.", http.StatusBadRequest)
			errChan <- errors.New("did not receive authorization code from provider")
			return
		}

		// Send a success message to the user's browser.
		fmt.Fprintln(w, "Authentication successful! You can close this browser tab now.")
		codeChan <- code
	})

	// Start the server in a separate goroutine.
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errChan <- fmt.Errorf("failed to start local server for oauth callback: %w", err)
		}
	}()

	// Wait for the code or an error.
	var code string
	select {
	case code = <-codeChan:
		logger.Info("Authorization code received successfully.")
	case err := <-errChan:
		return nil, err
	case <-time.After(5 * time.Minute): // Timeout for the whole process.
		return nil, errors.New("timed out waiting for oauth callback")
	}

	// Shutdown the server gracefully now that we have the code.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("Failed to gracefully shut down callback server: %v", err)
	}

	// Exchange the authorization code for an access and refresh token.
	token, err := conf.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange authorization code for token: %w", err)
	}

	return token, nil
}```