package cmd

import (
	"cloud-drives-sync/google"
	"cloud-drives-sync/microsoft"
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
	cfg, err := LoadConfig(promptForPassword())
	if err != nil {
		return false
	}
	for _, u := range cfg.Users {
		if u.Provider == provider && u.IsMain {
			return true
		}
	}
	return false
}

func getAuthURL(provider string) string {
	cfg, err := LoadConfig(promptForPassword())
	if err != nil {
		return ""
	}
	switch provider {
	case "Google":
		gd, _ := google.NewGoogleDrive(cfg.GoogleClient.ID, cfg.GoogleClient.Secret, "")
		return gd.GetAuthURL()
	case "Microsoft":
		ms, _ := microsoft.NewOneDrive(cfg.MicrosoftClient.ID, cfg.MicrosoftClient.Secret, "")
		return ms.GetAuthURL()
	}
	return ""
}

func exchangeCodeForToken(provider, code string) (string, string) {
	cfg, err := LoadConfig(promptForPassword())
	if err != nil {
		return "", ""
	}
	switch provider {
	case "Google":
		gd, _ := google.NewGoogleDrive(cfg.GoogleClient.ID, cfg.GoogleClient.Secret, "")
		refreshToken, email, err := gd.ExchangeCodeForToken(code)
		if err != nil {
			fmt.Println("[Add-Account] Google token exchange failed:", err)
			return "", ""
		}
		return refreshToken, email
	case "Microsoft":
		ms, _ := microsoft.NewOneDrive(cfg.MicrosoftClient.ID, cfg.MicrosoftClient.Secret, "")
		refreshToken, email, err := ms.ExchangeCodeForToken(code)
		if err != nil {
			fmt.Println("[Add-Account] Microsoft token exchange failed:", err)
			return "", ""
		}
		return refreshToken, email
	}
	return "", ""
}

func addBackupAccountToConfig(provider, email, refreshToken string) {
	cfg, err := LoadConfig(promptForPassword())
	if err != nil {
		fmt.Println("[Add-Account] Failed to load config:", err)
		return
	}
	cfg.Users = append(cfg.Users, struct {
		Provider     string `json:"provider"`
		Email        string `json:"email"`
		IsMain       bool   `json:"is_main"`
		RefreshToken string `json:"refresh_token"`
	}{Provider: provider, Email: email, IsMain: false, RefreshToken: refreshToken})
	if err := SaveConfig(cfg, promptForPassword()); err != nil {
		fmt.Println("[Add-Account] Failed to update config:", err)
	}
}

func shareSyncFolderWithBackup(provider, backupEmail string) {
	cfg, err := LoadConfig(promptForPassword())
	if err != nil {
		fmt.Println("[Add-Account] Failed to load config:", err)
		return
	}
	var mainEmail string
	for _, u := range cfg.Users {
		if u.Provider == provider && u.IsMain {
			mainEmail = u.Email
			break
		}
	}
	if mainEmail == "" {
		fmt.Println("[Add-Account] No main account found for sharing.")
		return
	}
	switch provider {
	case "Google":
		gd, _ := google.NewGoogleDrive(cfg.GoogleClient.ID, cfg.GoogleClient.Secret, getRefreshToken(cfg, provider, mainEmail))
		_ = gd.ShareSyncFolder(mainEmail, backupEmail)
	case "Microsoft":
		ms, _ := microsoft.NewOneDrive(cfg.MicrosoftClient.ID, cfg.MicrosoftClient.Secret, getRefreshToken(cfg, provider, mainEmail))
		_ = ms.ShareSyncFolder(mainEmail, backupEmail)
	}
}
