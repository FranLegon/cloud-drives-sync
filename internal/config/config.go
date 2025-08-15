package config

import (
	"cloud-drives-sync/internal/crypto"
	"cloud-drives-sync/internal/model"
	"encoding/json"
	"io/ioutil"
	"os"
	"path/filepath"
)

const (
	// ConfigFile is the name of the encrypted configuration file.
	ConfigFile = "config.json.enc"
	// SaltFile is the name of the file storing the salt for key derivation.
	SaltFile = "config.salt"
)

// Config holds the application's entire configuration, which is serialized
// to JSON before being encrypted and saved to disk.
type Config struct {
	GoogleClient    ClientCredentials `json:"google_client"`
	MicrosoftClient ClientCredentials `json:"microsoft_client"`
	Users           []model.User      `json:"users"`
}

// ClientCredentials holds the OAuth client ID and secret for a cloud provider.
type ClientCredentials struct {
	ID     string `json:"id"`
	Secret string `json:"secret"`
}

// GetConfigPath returns the absolute path to a configuration file, assuming
// it resides in the same directory as the executable.
func GetConfigPath(filename string) (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(exePath), filename), nil
}

// LoadConfig finds, reads, decrypts, and unmarshals the configuration from disk.
func LoadConfig(password string) (*Config, error) {
	saltPath, err := GetConfigPath(SaltFile)
	if err != nil {
		return nil, err
	}
	salt, err := ioutil.ReadFile(saltPath)
	if err != nil {
		return nil, err
	}

	configPath, err := GetConfigPath(ConfigFile)
	if err != nil {
		return nil, err
	}
	encData, err := ioutil.ReadFile(configPath)
	if err != nil {
		return nil, err
	}

	key := crypto.DeriveKey(password, salt)
	jsonData, err := crypto.Decrypt(encData, key)
	if err != nil {
		return nil, err
	}

	var config Config
	if err := json.Unmarshal(jsonData, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

// SaveConfig marshals the current Config struct to JSON, encrypts it,
// and writes it back to disk. It handles the initial creation of the salt file.
func (c *Config) SaveConfig(password string) error {
	saltPath, err := GetConfigPath(SaltFile)
	if err != nil {
		return err
	}

	salt, err := ioutil.ReadFile(saltPath)
	if err != nil {
		// If the salt file doesn't exist, this is the first save.
		if os.IsNotExist(err) {
			newSalt, genErr := crypto.GenerateSalt()
			if genErr != nil {
				return genErr
			}
			if writeErr := ioutil.WriteFile(saltPath, newSalt, 0600); writeErr != nil {
				return writeErr
			}
			salt = newSalt
		} else {
			// Any other error reading the salt is a problem.
			return err
		}
	}

	jsonData, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	key := crypto.DeriveKey(password, salt)
	encData, err := crypto.Encrypt(jsonData, key)
	if err != nil {
		return err
	}

	configPath, err := GetConfigPath(ConfigFile)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(configPath, encData, 0600)
}
