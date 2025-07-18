package cmd

import (
	"cloud-drives-sync/crypto"
	"crypto/rand"
	"encoding/json"
	"os"
)

type Config struct {
	GoogleClient struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	} `json:"google_client"`
	MicrosoftClient struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	} `json:"microsoft_client"`
	Users []struct {
		Provider     string `json:"provider"`
		Email        string `json:"email"`
		IsMain       bool   `json:"is_main"`
		RefreshToken string `json:"refresh_token"`
	} `json:"users"`
}

func LoadConfig(masterPassword string) (*Config, error) {
	encPath := "bin/config.json.enc"
	encData, err := os.ReadFile(encPath)
	if err != nil {
		return nil, err
	}
	// Assume first 16 bytes are salt, next 12 bytes are nonce, rest is ciphertext
	if len(encData) < 28 {
		return nil, os.ErrInvalid
	}
	salt := encData[:16]
	nonce := encData[16:28]
	ciphertext := encData[28:]
	key := crypto.DeriveKey([]byte(masterPassword), salt)
	plaintext, err := crypto.Decrypt(ciphertext, nonce, key)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(plaintext, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func SaveConfig(cfg *Config, masterPassword string) error {
	plain, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	key := crypto.DeriveKey([]byte(masterPassword), salt)
	ciphertext, nonce, err := crypto.Encrypt(plain, key)
	if err != nil {
		return err
	}
	out := append(salt, nonce...)
	out = append(out, ciphertext...)
	return os.WriteFile("bin/config.json.enc", out, 0600)
}
