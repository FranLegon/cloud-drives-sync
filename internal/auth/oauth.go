package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/FranLegon/cloud-drives-sync/internal/logger"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"golang.org/x/oauth2/microsoft"
)

const (
	callbackPort = 8080
	callbackPath = "/callback"
)

// OAuthServer represents a local OAuth callback server
type OAuthServer struct {
	server  *http.Server
	codeCh  chan string
	errCh   chan error
	stateCh chan string
}

// NewOAuthServer creates a new OAuth callback server
func NewOAuthServer() *OAuthServer {
	return &OAuthServer{
		codeCh:  make(chan string, 1),
		errCh:   make(chan error, 1),
		stateCh: make(chan string, 1),
	}
}

// Start starts the OAuth callback server
func (s *OAuthServer) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, s.handleCallback)

	s.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", callbackPort),
		Handler: mux,
	}

	go func() {
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.errCh <- err
		}
	}()

	logger.Info("OAuth callback server started on http://localhost:%d%s", callbackPort, callbackPath)
	return nil
}

// Stop stops the OAuth callback server
func (s *OAuthServer) Stop() error {
	if s.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.server.Shutdown(ctx)
	}
	return nil
}

// WaitForCode waits for the authorization code
func (s *OAuthServer) WaitForCode(expectedState string, timeout time.Duration) (string, error) {
	select {
	case code := <-s.codeCh:
		// Verify state
		select {
		case state := <-s.stateCh:
			if state != expectedState {
				return "", fmt.Errorf("state mismatch: expected %s, got %s", expectedState, state)
			}
		case <-time.After(1 * time.Second):
			return "", fmt.Errorf("state verification timeout")
		}
		return code, nil
	case err := <-s.errCh:
		return "", err
	case <-time.After(timeout):
		return "", fmt.Errorf("authorization timeout")
	}
}

// handleCallback handles the OAuth callback
func (s *OAuthServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	errorParam := r.URL.Query().Get("error")

	if errorParam != "" {
		s.errCh <- fmt.Errorf("OAuth error: %s", errorParam)
		fmt.Fprintf(w, "Authorization failed: %s. You can close this window.", errorParam)
		return
	}

	if code == "" {
		s.errCh <- fmt.Errorf("no authorization code received")
		fmt.Fprintf(w, "Authorization failed: no code received. You can close this window.")
		return
	}

	s.codeCh <- code
	s.stateCh <- state
	fmt.Fprintf(w, "Authorization successful! You can close this window and return to the terminal.")
}

// GetGoogleOAuthConfig returns an OAuth2 config for Google Drive
func GetGoogleOAuthConfig(clientID, clientSecret string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  fmt.Sprintf("http://localhost:%d%s", callbackPort, callbackPath),
		Scopes: []string{
			"https://www.googleapis.com/auth/drive",
			"https://www.googleapis.com/auth/drive.file",
			"https://www.googleapis.com/auth/drive.metadata",
			"https://www.googleapis.com/auth/userinfo.email",
		},
		Endpoint: google.Endpoint,
	}
}

// GetMicrosoftOAuthConfig returns an OAuth2 config for Microsoft OneDrive
func GetMicrosoftOAuthConfig(clientID, clientSecret string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  fmt.Sprintf("http://localhost:%d%s", callbackPort, callbackPath),
		Scopes: []string{
			"files.readwrite.all",
			"user.read",
			"offline_access",
		},
		Endpoint: microsoft.AzureADEndpoint("common"),
	}
}

// ExchangeCode exchanges an authorization code for tokens
func ExchangeCode(ctx context.Context, config *oauth2.Config, code string) (*oauth2.Token, error) {
	token, err := config.Exchange(ctx, code)
	if err != nil {
		return nil, err
	}
	return token, nil
}

// GetUserEmail retrieves the user's email from Google
func GetGoogleUserEmail(ctx context.Context, token *oauth2.Token, config *oauth2.Config) (string, error) {
	client := config.Client(ctx, token)
	resp, err := client.Get("https://www.googleapis.com/oauth2/v2/userinfo")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var userInfo struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
		return "", err
	}

	return userInfo.Email, nil
}

// GetMicrosoftUserEmail retrieves the user's email from Microsoft
func GetMicrosoftUserEmail(ctx context.Context, token *oauth2.Token, config *oauth2.Config) (string, error) {
	client := config.Client(ctx, token)
	resp, err := client.Get("https://graph.microsoft.com/v1.0/me")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var userInfo struct {
		UserPrincipalName string `json:"userPrincipalName"`
		Mail              string `json:"mail"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
		return "", err
	}

	// Prefer mail over userPrincipalName
	if userInfo.Mail != "" {
		return userInfo.Mail, nil
	}
	return userInfo.UserPrincipalName, nil
}

// GenerateStateToken generates a random state token for OAuth
func GenerateStateToken() string {
	return fmt.Sprintf("state-%d", time.Now().UnixNano())
}
