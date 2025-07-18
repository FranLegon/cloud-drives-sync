package cmd

import (
	gdrive "cloud-drives-sync/google"
	msdrive "cloud-drives-sync/microsoft"
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize configuration and database",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("[Init] Initializing configuration and database...")
		// Prompt for master password
		var masterPassword string
		fmt.Print("Enter master password: ")
		fmt.Scanln(&masterPassword)

		// Check if config exists
		configPath := "bin/config.json.enc"
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			// Prompt for GCP and Azure credentials
			var gcpID, gcpSecret, azureID, azureSecret string
			fmt.Print("Enter Google Client ID: ")
			fmt.Scanln(&gcpID)
			fmt.Print("Enter Google Client Secret: ")
			fmt.Scanln(&gcpSecret)
			fmt.Print("Enter Microsoft Client ID: ")
			fmt.Scanln(&azureID)
			fmt.Print("Enter Microsoft Client Secret: ")
			fmt.Scanln(&azureSecret)
			cfg := &Config{}
			cfg.GoogleClient.ID = gcpID
			cfg.GoogleClient.Secret = gcpSecret
			cfg.MicrosoftClient.ID = azureID
			cfg.MicrosoftClient.Secret = azureSecret
			// Save config
			if err := SaveConfig(cfg, masterPassword); err != nil {
				fmt.Println("[Init] Failed to save config:", err)
				os.Exit(1)
			}
			fmt.Println("[Init] Config created and encrypted.")
		}

		// Create DB if not exists
		dbPath := "bin/metadata.db"
		if _, err := os.Stat(dbPath); os.IsNotExist(err) {
			db := getDatabase()
			if db == nil {
				fmt.Println("[Init] Failed to initialize database.")
				os.Exit(1)
			}
			db.Close()
			fmt.Println("[Init] Database created.")
		}

		// Load config and prompt to add main accounts if not present
		cfg, err := LoadConfig(masterPassword)
		if err != nil {
			fmt.Println("[Init] Failed to load config:", err)
			os.Exit(1)
		}
		for _, provider := range []string{"Google", "Microsoft"} {
			mainExists := false
			for _, u := range cfg.Users {
				if u.Provider == provider && u.IsMain {
					mainExists = true
					break
				}
			}
			if !mainExists {
				fmt.Printf("Add main account for %s? (y/n): ", provider)
				var yn string
				fmt.Scanln(&yn)
				if yn == "y" || yn == "Y" {
					fmt.Println("Starting local server on http://localhost:8080/oauth2callback ...")
					codeCh := make(chan string)
					server := &http.Server{Addr: ":8080"}
					http.HandleFunc("/oauth2callback", func(w http.ResponseWriter, r *http.Request) {
						code := r.URL.Query().Get("code")
						fmt.Fprintf(w, "Authorization received. You may close this window.")
						codeCh <- code
					})
					go func() { server.ListenAndServe() }()
					var authURL string
					switch provider {
					case "Google":
						gd, _ := gdrive.NewGoogleDrive(cfg.GoogleClient.ID, cfg.GoogleClient.Secret, "")
						authURL = gd.GetAuthURL()
					case "Microsoft":
						ms, _ := msdrive.NewOneDrive(cfg.MicrosoftClient.ID, cfg.MicrosoftClient.Secret, "")
						authURL = ms.GetAuthURL()
					}
					fmt.Printf("Visit this URL to authorize: %s\n", authURL)
					code := <-codeCh
					server.Close()
					var refreshToken, email string
					switch provider {
					case "Google":
						gd, _ := gdrive.NewGoogleDrive(cfg.GoogleClient.ID, cfg.GoogleClient.Secret, "")
						refreshToken, email, _ = gd.ExchangeCodeForToken(code)
					case "Microsoft":
						ms, _ := msdrive.NewOneDrive(cfg.MicrosoftClient.ID, cfg.MicrosoftClient.Secret, "")
						refreshToken, email, _ = ms.ExchangeCodeForToken(code)
					}
					cfg.Users = append(cfg.Users, struct {
						Provider     string `json:"provider"`
						Email        string `json:"email"`
						IsMain       bool   `json:"is_main"`
						RefreshToken string `json:"refresh_token"`
					}{Provider: provider, Email: email, IsMain: true, RefreshToken: refreshToken})
					if err := SaveConfig(cfg, masterPassword); err != nil {
						fmt.Println("[Init] Failed to update config:", err)
						os.Exit(1)
					}
					fmt.Printf("[Init] Main account for %s added.\n", provider)
				}
			}
		}

		// For each main account, create synched-cloud-drives folder if not present
		for _, u := range cfg.Users {
			if u.IsMain {
				switch u.Provider {
				case "Google":
					fmt.Printf("[Init][Google][%s] Ensuring synched-cloud-drives folder exists...\n", u.Email)
					err := ensureGoogleSyncFolder(u.Email, cfg.GoogleClient.ID, cfg.GoogleClient.Secret, u.RefreshToken)
					if err != nil {
						fmt.Printf("[Init][Google][%s] Error: %v\n", u.Email, err)
						os.Exit(1)
					}
					fmt.Printf("[Init][Google][%s] synched-cloud-drives folder ready.\n", u.Email)
				case "Microsoft":
					fmt.Printf("[Init][Microsoft][%s] Ensuring synched-cloud-drives folder exists...\n", u.Email)
					err := ensureMicrosoftSyncFolder(u.Email, cfg.MicrosoftClient.ID, cfg.MicrosoftClient.Secret, u.RefreshToken)
					if err != nil {
						fmt.Printf("[Init][Microsoft][%s] Error: %v\n", u.Email, err)
						os.Exit(1)
					}
					fmt.Printf("[Init][Microsoft][%s] synched-cloud-drives folder ready.\n", u.Email)
				}
			}
		}
		fmt.Println("[Init] Initialization complete.")
	},
}

func init() {
	rootCmd.AddCommand(initCmd)
}

// ensureGoogleSyncFolder ensures the synched-cloud-drives folder exists for the main Google account

func ensureGoogleSyncFolder(email, clientID, clientSecret, refreshToken string) error {
	// Create a GoogleDrive client (assume NewGoogleDrive returns GoogleDrive interface)
	gd, err := gdrive.NewGoogleDrive(clientID, clientSecret, refreshToken)
	if err != nil {
		return err
	}
	// Pre-flight check: ensure exactly one folder, move if needed, create if missing
	folders, err := gd.ListFolders(email, "synched-cloud-drives")
	if err != nil {
		return err
	}
	if len(folders) == 0 {
		// Create folder
		if err := gd.CreateSyncFolder(email); err != nil {
			return err
		}
	} else if len(folders) > 1 {
		return fmt.Errorf("found %d 'synched-cloud-drives' folders. Resolve manually", len(folders))
	} else if !folders[0].IsRoot {
		if err := gd.MoveFolderToRoot(email, folders[0].ID); err != nil {
			return err
		}
	}
	return nil
}

func ensureMicrosoftSyncFolder(email, clientID, clientSecret, refreshToken string) error {
	// Create a OneDrive client (assume NewOneDrive returns OneDrive interface)
	ms, err := msdrive.NewOneDrive(clientID, clientSecret, refreshToken)
	if err != nil {
		return err
	}
	folders, err := ms.ListFolders(email, "synched-cloud-drives")
	if err != nil {
		return err
	}
	if len(folders) == 0 {
		if err := ms.CreateSyncFolder(email); err != nil {
			return err
		}
	} else if len(folders) > 1 {
		return fmt.Errorf("found %d 'synched-cloud-drives' folders. Resolve manually", len(folders))
	} else if !folders[0].IsRoot {
		if err := ms.MoveFolderToRoot(email, folders[0].ID); err != nil {
			return err
		}
	}
	return nil
}
