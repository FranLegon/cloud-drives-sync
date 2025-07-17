package cmd

import (
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"
)

var addAccountCmd = &cobra.Command{
	Use:   "add-account",
	Short: "Add a backup Google or Microsoft account",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("[Add-Account] Starting account addition...")
		// Prompt user for provider
		var provider string
		fmt.Print("Choose provider (Google/Microsoft): ")
		fmt.Scanln(&provider)
		provider = normalizeProvider(provider)
		if provider != "Google" && provider != "Microsoft" {
			fmt.Println("Invalid provider. Must be 'Google' or 'Microsoft'.")
			os.Exit(1)
		}
		// Check if main account exists in config
		mainExists := checkMainAccountExists(provider)
		if !mainExists {
			fmt.Printf("No main account for %s found. Please add main account first using 'init'.\n", provider)
			os.Exit(1)
		}
		// Start local web server for OAuth2 callback
		fmt.Println("Starting local server on http://localhost:8080/oauth2callback ...")
		codeCh := make(chan string)
		server := &http.Server{Addr: ":8080"}
		http.HandleFunc("/oauth2callback", func(w http.ResponseWriter, r *http.Request) {
			code := r.URL.Query().Get("code")
			fmt.Fprintf(w, "Authorization received. You may close this window.")
			codeCh <- code
		})
		go func() { server.ListenAndServe() }()
		// Print authorization URL
		authURL := getAuthURL(provider)
		fmt.Printf("Visit this URL to authorize: %s\n", authURL)
		code := <-codeCh
		server.Close()
		// Exchange code for refresh token
		refreshToken, email := exchangeCodeForToken(provider, code)
		// Add to config
		addBackupAccountToConfig(provider, email, refreshToken)
		fmt.Printf("Backup account %s added for %s.\n", email, provider)
		// Share sync folder
		shareSyncFolderWithBackup(provider, email)
		fmt.Printf("Sync folder shared with backup account %s.\n", email)
	},
}

func init() {
	rootCmd.AddCommand(addAccountCmd)
}

// Helper functions (implementations would use google/microsoft packages and config management)
func normalizeProvider(p string) string {
	if p == "google" || p == "Google" {
		return "Google"
	}
	if p == "microsoft" || p == "Microsoft" {
		return "Microsoft"
	}
	return p
}

func checkMainAccountExists(provider string) bool {
	// ...existing code to check config...
	return true // stub
}

func getAuthURL(provider string) string {
	// ...existing code to get auth URL...
	return "http://example.com/auth"
}

func exchangeCodeForToken(provider, code string) (string, string) {
	// ...existing code to exchange code for token...
	return "refresh_token", "backup@example.com"
}

func addBackupAccountToConfig(provider, email, refreshToken string) {
	// ...existing code to update config...
}

func shareSyncFolderWithBackup(provider, backupEmail string) {
	// ...existing code to share folder...
}
