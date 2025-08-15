package auth

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"cloud-drives-sync/internal/config"
	"cloud-drives-sync/internal/logger"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"golang.org/x/oauth2/microsoft"
)

const (
	redirectURL = "http://localhost:8080/oauth/callback"
)

// StartOAuthFlow initiates the full OAuth 2.0 authorization code flow for a given provider.
// It starts a local web server to receive the callback, opens the user's browser,
// waits for the authorization code, and exchanges it for a refresh token.
func StartOAuthFlow(provider string, cfg *config.Config) (string, error) {
	var oauthCfg *oauth2.Config

	switch provider {
	case "Google":
		oauthCfg = &oauth2.Config{
			ClientID:     cfg.GoogleClient.ID,
			ClientSecret: cfg.GoogleClient.Secret,
			RedirectURL:  redirectURL,
			Endpoint:     google.Endpoint,
			Scopes: []string{
				"https://www.googleapis.com/auth/drive",
				"https://www.googleapis.com/auth/userinfo.email",
			},
		}
	case "Microsoft":
		oauthCfg = &oauth2.Config{
			ClientID:     cfg.MicrosoftClient.ID,
			ClientSecret: cfg.MicrosoftClient.Secret,
			RedirectURL:  redirectURL,
			// *** THIS IS THE FIX ***
			// Use the "common" endpoint to support both personal and work/school accounts.
			Endpoint: microsoft.AzureADEndpoint("common"),
			Scopes: []string{
				"files.readwrite.all",
				"user.read",
				"offline_access", // This scope is required to get a refresh token.
			},
		}
	default:
		return "", fmt.Errorf("unknown provider for OAuth flow: %s", provider)
	}

	// `ApprovalForce` ensures a refresh token is issued even if the user has previously consented.
	authURL := oauthCfg.AuthCodeURL("state-token", oauth2.AccessTypeOffline, oauth2.ApprovalForce)

	codeChan := make(chan string)
	errChan := make(chan error)

	server := &http.Server{Addr: ":8080"}
	http.HandleFunc("/oauth/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			msg := "Authorization code not found in callback URL."
			fmt.Fprintln(w, "Error: "+msg+". Please try again.")
			errChan <- fmt.Errorf(msg)
			return
		}
		fmt.Fprintln(w, "Authentication successful! You can close this browser window and return to the terminal.")
		codeChan <- code
	})

	go func() {
		// This server will block until it's shut down.
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			errChan <- fmt.Errorf("callback server error: %w", err)
		}
	}()
	// Gracefully shut down the server when the function exits.
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(ctx)
	}()

	logger.Info("Your browser should open for authentication.")
	logger.Info("If it doesn't, please open this URL:\n%s", authURL)
	time.Sleep(1 * time.Second) // Give the server a moment to start up.
	if err := openBrowser(authURL); err != nil {
		logger.Warn("auth", err, "could not open browser automatically")
	}

	select {
	case code := <-codeChan:
		// Exchange the authorization code for a token set (which includes the refresh token).
		token, err := oauthCfg.Exchange(context.Background(), code)
		if err != nil {
			return "", fmt.Errorf("failed to exchange authorization code for token: %w", err)
		}
		if token.RefreshToken == "" {
			return "", fmt.Errorf("a refresh token was not returned. Please ensure you are requesting 'offline_access' and the Azure App Registration is configured correctly")
		}
		return token.RefreshToken, nil
	case err := <-errChan:
		return "", err
	case <-time.After(3 * time.Minute): // Timeout for the whole process.
		return "", fmt.Errorf("authentication timed out after 3 minutes")
	}
}

// openBrowser attempts to open a URL in the user's default web browser.
func openBrowser(url string) error {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "windows":
		cmd = "cmd"
		args = []string{"/c", "start"}
	case "darwin":
		cmd = "open"
	default: // "linux", "freebsd", "openbsd", "netbsd"
		cmd = "xdg-open"
	}
	args = append(args, url)
	// Start the command but don't wait for it to complete.
	return exec.Command(cmd, args...).Start()
}
