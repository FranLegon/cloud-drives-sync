package config

import (
	"bufio"
	"cloud-drives-sync/crypto"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type ClientCreds struct {
	ID     string `json:"id"`
	Secret string `json:"secret"`
}

type User struct {
	Provider     string `json:"provider"`
	Email        string `json:"email"`
	IsMain       bool   `json:"is_main"`
	RefreshToken string `json:"refresh_token"`
}

type Config struct {
	GoogleClient    ClientCreds `json:"google_client"`
	MicrosoftClient ClientCreds `json:"microsoft_client"`
	Users           []User      `json:"users"`
}

func EncryptAndSaveConfig(cfg Config, path, password string) error {
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	enc, err := crypto.Encrypt(raw, password)
	if err != nil {
		return err
	}
	return os.WriteFile(path, enc, 0600)
}

func LoadConfigWithPassword(path string) (Config, string, error) {
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("Enter master password: ")
	pw, _ := reader.ReadString('\n')
	pw = strings.TrimSpace(pw)
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, "", err
	}
	dec, err := crypto.Decrypt(raw, pw)
	if err != nil {
		return Config{}, "", err
	}
	var cfg Config
	if err := json.Unmarshal(dec, &cfg); err != nil {
		return Config{}, "", err
	}
	return cfg, pw, nil
}

func (c *Config) HasMainAccount(provider string) bool {
	for _, u := range c.Users {
		if u.Provider == provider && u.IsMain {
			return true
		}
	}
	return false
}

func (c *Config) GetMainAccount(provider string) *User {
	for i := range c.Users {
		u := &c.Users[i]
		if u.Provider == provider && u.IsMain {
			return u
		}
	}
	return nil
}

func (c *Config) GetAccounts(provider string) []User {
	var out []User
	for _, u := range c.Users {
		if u.Provider == provider {
			out = append(out, u)
		}
	}
	return out
}

func (c *Config) Save(path, password string) error {
	return EncryptAndSaveConfig(*c, path, password)
}
