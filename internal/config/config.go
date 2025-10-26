package config

import (
	"encoding/json"
	"errors"
	"os"

	"cloud-drives-sync/internal/crypto"
	"cloud-drives-sync/internal/model"
)

const (
	ConfigFileName = "config.json.enc"
)

// ClientCredentials represents OAuth client credentials
type ClientCredentials struct {
	ID     string `json:"id"`
	Secret string `json:"secret"`
}

// Config represents the application configuration
type Config struct {
	GoogleClient    ClientCredentials `json:"google_client"`
	MicrosoftClient ClientCredentials `json:"microsoft_client"`
	Users           []model.User      `json:"users"`
}

// Load reads and decrypts the configuration file
func Load(password string) (*Config, error) {
	// Load salt
	salt, err := crypto.LoadSalt(crypto.SaltFileName)
	if err != nil {
		return nil, errors.New("failed to load salt file: " + err.Error())
	}

	// Derive encryption key
	key := crypto.DeriveKey(password, salt)

	// Read encrypted config file
	encryptedData, err := os.ReadFile(ConfigFileName)
	if err != nil {
		return nil, errors.New("failed to read config file: " + err.Error())
	}

	// Decrypt
	decryptedData, err := crypto.Decrypt(encryptedData, key)
	if err != nil {
		return nil, errors.New("failed to decrypt config (wrong password?): " + err.Error())
	}

	// Unmarshal JSON
	var cfg Config
	if err := json.Unmarshal(decryptedData, &cfg); err != nil {
		return nil, errors.New("failed to parse config JSON: " + err.Error())
	}

	return &cfg, nil
}

// Save encrypts and writes the configuration file
func Save(cfg *Config, password string) error {
	// Load salt
	salt, err := crypto.LoadSalt(crypto.SaltFileName)
	if err != nil {
		return errors.New("failed to load salt file: " + err.Error())
	}

	// Derive encryption key
	key := crypto.DeriveKey(password, salt)

	// Marshal to JSON
	jsonData, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return errors.New("failed to marshal config to JSON: " + err.Error())
	}

	// Encrypt
	encryptedData, err := crypto.Encrypt(jsonData, key)
	if err != nil {
		return errors.New("failed to encrypt config: " + err.Error())
	}

	// Write to file
	if err := os.WriteFile(ConfigFileName, encryptedData, 0600); err != nil {
		return errors.New("failed to write config file: " + err.Error())
	}

	return nil
}

// GetMainAccount returns the main account for a given provider
func (c *Config) GetMainAccount(provider model.Provider) (*model.User, error) {
	for i := range c.Users {
		if c.Users[i].Provider == provider && c.Users[i].IsMain {
			return &c.Users[i], nil
		}
	}
	return nil, errors.New("no main account found for provider: " + string(provider))
}

// GetBackupAccounts returns all backup accounts for a given provider
func (c *Config) GetBackupAccounts(provider model.Provider) []model.User {
	var backups []model.User
	for _, user := range c.Users {
		if user.Provider == provider && !user.IsMain {
			backups = append(backups, user)
		}
	}
	return backups
}

// GetAllAccounts returns all accounts for a given provider
func (c *Config) GetAllAccounts(provider model.Provider) []model.User {
	var accounts []model.User
	for _, user := range c.Users {
		if user.Provider == provider {
			accounts = append(accounts, user)
		}
	}
	return accounts
}

// AddUser adds a new user to the configuration
func (c *Config) AddUser(user model.User) {
	c.Users = append(c.Users, user)
}

// HasMainAccount checks if a main account exists for a provider
func (c *Config) HasMainAccount(provider model.Provider) bool {
	_, err := c.GetMainAccount(provider)
	return err == nil
}
