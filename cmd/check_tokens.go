package cmd

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/config"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/logger"
)

// checkTokensCmd represents the check-tokens command
var checkTokensCmd = &cobra.Command{
	Use:   "check-tokens",
	Short: "Checks the validity of all stored authentication tokens",
	Long: `This command iterates through every configured user account and attempts to use its
stored refresh token to make a simple, read-only API call.

It reports the status for each token, allowing you to quickly identify any accounts
whose authentication has expired or has been revoked. If a token is invalid, you will
likely need to remove and re-add the account to get a new one.`,
	Run: runCheckTokens,
}

func runCheckTokens(cmd *cobra.Command, args []string) {
	logger.Info("Checking the status of all configured account tokens...")

	// --- Step 1: Load Config and Get Password ---
	masterPassword, err := config.GetMasterPassword(false)
	if err != nil {
		logger.Fatal("Failed to get master password: %v", err)
	}
	appCfg, err := config.LoadConfig(masterPassword)
	if err != nil {
		logger.Fatal("Failed to load config: %v", err)
	}

	if len(appCfg.Users) == 0 {
		logger.Info("No accounts configured. Nothing to check.")
		return
	}

	// --- Step 2: Concurrently Check Each Token ---
	var wg sync.WaitGroup
	results := make(chan string, len(appCfg.Users))

	for _, user := range appCfg.Users {
		wg.Add(1)
		go func(user config.User) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			// Create a client for the user. The act of creating the token source
			// and the first API call will test the refresh token.
			client, err := getClientForUser(ctx, &user, appCfg)
			if err != nil {
				// This can happen if the token is invalid from the start.
				results <- fmt.Sprintf("❌ [%s] %-25s: INVALID (Failed to create client: %v)", user.Provider, user.Email, err)
				return
			}

			// Perform a simple read-only call to verify the token works.
			_, err = client.GetUserInfo(ctx)
			if err != nil {
				results <- fmt.Sprintf("❌ [%s] %-25s: INVALID (API call failed: %v)", user.Provider, user.Email, err)
			} else {
				results <- fmt.Sprintf("✅ [%s] %-25s: VALID", user.Provider, user.Email)
			}
		}(user)
	}

	// --- Step 3: Wait and Report Results ---
	wg.Wait()
	close(results)

	logger.Info("\n--- Token Status Report ---")
	for res := range results {
		fmt.Println(res)
	}
	logger.Info("---------------------------")
	logger.Info("Token check complete.")
}
