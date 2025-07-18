package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"golang.org/x/crypto/argon2"
)

const (
	// saltFile is the name of the file where the unique cryptographic salt is stored.
	saltFile = "config.salt"
)

// GenerateSalt creates a new 32-byte cryptographically secure salt and saves it
// to the salt file, overwriting it if it exists. Permissions are set to be
// readable only by the current user.
func GenerateSalt() ([]byte, error) {
	salt := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, fmt.Errorf("failed to generate random salt: %w", err)
	}

	err := os.WriteFile(saltFile, salt, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to write salt file: %w", err)
	}
	return salt, nil
}

// GetSalt reads the cryptographic salt from the pre-defined salt file.
func GetSalt() ([]byte, error) {
	salt, err := os.ReadFile(saltFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read salt file '%s': %w", saltFile, err)
	}
	return salt, nil
}

// DeriveKey generates a 32-byte (256-bit) key from a user's password and a
// salt using the Argon2id key derivation function. The parameters used are
// based on current OWASP recommendations for secure key derivation. [11, 25]
func DeriveKey(password string, salt []byte) []byte {
	// Argon2id parameters: time=1, memory=64MB, threads=4, key length=32 bytes.
	return argon2.IDKey([]byte(password), salt, 1, 64*1024, 4, 32)
}

// Encrypt takes a 32-byte key and a data structure, marshals the data to JSON,
// and encrypts it using AES-256 GCM. The nonce is prepended to the ciphertext. [7, 31]
func Encrypt(key []byte, data interface{}) ([]byte, error) {
	plaintext, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal data to JSON: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM cipher mode: %w", err)
	}

	// We generate a new nonce for each encryption.
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("failed to generate nonce: %w", err)
	}

	// GCM's Seal function handles the encryption and authentication.
	// We prepend the nonce to the ciphertext for use during decryption.
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return ciphertext, nil
}

// Decrypt takes a 32-byte key and a ciphertext (with nonce prepended) and
// decrypts it using AES-256 GCM. The resulting JSON is unmarshalled into the
// provided `data` interface. [7, 24]
func Decrypt(key []byte, ciphertext []byte, data interface{}) error {
	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("failed to create AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("failed to create GCM cipher mode: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return fmt.Errorf("ciphertext is too short to contain a nonce")
	}

	// The nonce is sliced from the beginning of the ciphertext.
	nonce, actualCiphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]

	plaintext, err := gcm.Open(nil, nonce, actualCiphertext, nil)
	if err != nil {
		// This error can indicate a wrong key or tampered ciphertext.
		return fmt.Errorf("failed to decrypt or authenticate data: %w", err)
	}

	if err := json.Unmarshal(plaintext, data); err != nil {
		return fmt.Errorf("failed to unmarshal decrypted JSON: %w", err)
	}

	return nil
}
