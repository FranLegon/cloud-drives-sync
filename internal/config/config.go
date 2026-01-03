package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/FranLegon/cloud-drives-sync/internal/crypto"
	"github.com/FranLegon/cloud-drives-sync/internal/model"
)

const (
	ConfigFileName = "config.json.enc"
	SaltFileName   = "config.salt"
)

var (
	ErrConfigNotFound = errors.New("configuration file not found")
	ErrInvalidPassword = errors.New("invalid master password")
)

// GetConfigPath returns the path to the config file
func GetConfigPath() string {
	execPath, err := os.Executable()
	if err != nil {
		return ConfigFileName
	}
	return filepath.Join(filepath.Dir(execPath), ConfigFileName)
}

// GetSaltPath returns the path to the salt file
func GetSaltPath() string {
	execPath, err := os.Executable()
	if err != nil {
		return SaltFileName
	}
	return filepath.Join(filepath.Dir(execPath), SaltFileName)
}

// LoadConfig loads and decrypts the configuration file
func LoadConfig(masterPassword string) (*model.Config, error) {
	saltPath := GetSaltPath()
	configPath := GetConfigPath()

	// Load salt
	salt, err := crypto.LoadSalt(saltPath)
	if err != nil {
		return nil, err
	}

	// Derive key
	key := crypto.DeriveKey(masterPassword, salt)

	// Load encrypted config
	encryptedData, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrConfigNotFound
		}
		return nil, err
	}

	// Decrypt config
	decryptedData, err := crypto.Decrypt(string(encryptedData), key)
	if err != nil {
		return nil, ErrInvalidPassword
	}

	// Parse JSON
	var cfg model.Config
	if err := json.Unmarshal(decryptedData, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// SaveConfig encrypts and saves the configuration file
func SaveConfig(cfg *model.Config, masterPassword string) error {
	saltPath := GetSaltPath()
	configPath := GetConfigPath()

	// Load or generate salt
	var salt []byte
	var err error
	
	if _, err := os.Stat(saltPath); os.IsNotExist(err) {
		salt, err = crypto.GenerateAndSaveSalt(saltPath)
		if err != nil {
			return err
		}
	} else {
		salt, err = crypto.LoadSalt(saltPath)
		if err != nil {
			return err
		}
	}

	// Derive key
	key := crypto.DeriveKey(masterPassword, salt)

	// Marshal config to JSON
	jsonData, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	// Encrypt config
	encryptedData, err := crypto.Encrypt(jsonData, key)
	if err != nil {
		return err
	}

	// Save encrypted config
	if err := os.WriteFile(configPath, []byte(encryptedData), 0600); err != nil {
		return err
	}

	return nil
}

// ConfigExists checks if the config file exists
func ConfigExists() bool {
	configPath := GetConfigPath()
	_, err := os.Stat(configPath)
	return err == nil
}

// SaltExists checks if the salt file exists
func SaltExists() bool {
	saltPath := GetSaltPath()
	_, err := os.Stat(saltPath)
	return err == nil
}

// GetMainAccount returns the main account for a specific provider
func GetMainAccount(cfg *model.Config, provider model.Provider) *model.User {
	for i := range cfg.Users {
		if cfg.Users[i].Provider == provider && cfg.Users[i].IsMain {
			return &cfg.Users[i]
		}
	}
	return nil
}

// GetBackupAccounts returns all backup accounts for a specific provider
func GetBackupAccounts(cfg *model.Config, provider model.Provider) []model.User {
	var accounts []model.User
	for _, user := range cfg.Users {
		if user.Provider == provider && !user.IsMain {
			accounts = append(accounts, user)
		}
	}
	return accounts
}

// GetAllAccounts returns all accounts for a specific provider
func GetAllAccounts(cfg *model.Config, provider model.Provider) []model.User {
	var accounts []model.User
	for _, user := range cfg.Users {
		if user.Provider == provider {
			accounts = append(accounts, user)
		}
	}
	return accounts
}

// AddUser adds a user to the configuration
func AddUser(cfg *model.Config, user model.User) {
	cfg.Users = append(cfg.Users, user)
}

// UpdateUser updates a user in the configuration
func UpdateUser(cfg *model.Config, user model.User) error {
	for i := range cfg.Users {
		if cfg.Users[i].Provider == user.Provider {
			if (user.Email != "" && cfg.Users[i].Email == user.Email) ||
				(user.Phone != "" && cfg.Users[i].Phone == user.Phone) {
				cfg.Users[i] = user
				return nil
			}
		}
	}
	return errors.New("user not found")
}
