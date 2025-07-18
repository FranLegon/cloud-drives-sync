package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/manifoldco/promptui"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/crypto"
	"github.com/sujipallapothu/go-sqlcipher/v4/cloud-drives-sync/internal/model"
)

const (
	// configFile is the name of the encrypted configuration file.
	configFile = "config.json.enc"
)

// AppConfig defines the structure for all configuration data, including client
// credentials and user accounts. It's the structure that gets serialized to
// and from the encrypted config file.
type AppConfig struct {
	GoogleClient    ClientCredentials `json:"google_client"`
	MicrosoftClient ClientCredentials `json:"microsoft_client"`
	Users           []model.User      `json:"users"`
}

// ClientCredentials holds the OAuth 2.0 client ID and secret for a cloud provider's API.
type ClientCredentials struct {
	ID     string `json:"id"`
	Secret string `json:"secret"`
}

// LoadConfig decrypts and loads the application configuration from `config.json.enc`.
// It requires the user's master password to derive the decryption key.
func LoadConfig(masterPassword string) (*AppConfig, error) {
	salt, err := crypto.GetSalt()
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errors.New("salt file not found. please run the 'init' command first")
		}
		return nil, fmt.Errorf("failed to read salt file: %w", err)
	}

	key := crypto.DeriveKey(masterPassword, salt)

	ciphertext, err := os.ReadFile(configFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errors.New("config file not found. please run the 'init' command first")
		}
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg AppConfig
	if err := crypto.Decrypt(key, ciphertext, &cfg); err != nil {
		return nil, errors.New("failed to decrypt config: master password may be incorrect")
	}

	return &cfg, nil
}

// SaveConfig encrypts the provided AppConfig structure and saves it to `config.json.enc`.
// It requires the user's master password to derive the encryption key.
func SaveConfig(masterPassword string, cfg *AppConfig) error {
	salt, err := crypto.GetSalt()
	if err != nil {
		return fmt.Errorf("failed to read salt before saving config: %w", err)
	}

	key := crypto.DeriveKey(masterPassword, salt)

	ciphertext, err := crypto.Encrypt(key, cfg)
	if err != nil {
		return fmt.Errorf("failed to encrypt config for saving: %w", err)
	}

	// Write with permissions that only allow the current user to read/write.
	return os.WriteFile(configFile, ciphertext, 0600)
}

// GetMasterPassword securely prompts the user to enter their master password
// without echoing the characters to the terminal.
func GetMasterPassword(confirm bool) (string, error) {
	validate := func(input string) error {
		if len(input) < 8 {
			return errors.New("password must be at least 8 characters long")
		}
		return nil
	}

	prompt := promptui.Prompt{
		Label:    "Enter Master Password",
		Mask:     '*',
		Validate: validate,
	}

	password, err := prompt.Run()
	if err != nil {
		return "", err
	}

	if confirm {
		confirmPrompt := promptui.Prompt{
			Label:    "Confirm Master Password",
			Mask:     '*',
			Validate: validate,
		}
		confirmation, err := confirmPrompt.Run()
		if err != nil {
			return "", err
		}
		if password != confirmation {
			return "", errors.New("passwords do not match")
		}
	}

	return password, nil
}
