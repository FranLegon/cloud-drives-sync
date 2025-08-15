package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"io"

	"golang.org/x/crypto/argon2"
)

// DeriveKey generates a 32-byte key from a password and salt using Argon2id.
// The parameters (time=1, memory=64MB, threads=4) are recommended by OWASP.
// This key size is suitable for AES-256.
func DeriveKey(password string, salt []byte) []byte {
	return argon2.IDKey([]byte(password), salt, 1, 64*1024, 4, 32)
}

// GenerateSalt creates a cryptographically secure 16-byte salt for key derivation.
func GenerateSalt() ([]byte, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return salt, nil
}

// Encrypt encrypts data using the AES-256 GCM authenticated encryption mode.
// A new, random nonce is generated and prepended to the ciphertext for each encryption.
func Encrypt(data []byte, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	// Seal will append the encrypted data and an authentication tag to the nonce.
	// The result is nonce || ciphertext || tag.
	ciphertext := gcm.Seal(nonce, nonce, data, nil)
	return ciphertext, nil
}

// Decrypt decrypts data using AES-256 GCM.
// It assumes the nonce is prepended to the ciphertext.
func Decrypt(data []byte, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return nil, errors.New("ciphertext is too short")
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		// This error is critical as it indicates either a corrupted file
		// or, more likely, an incorrect master password.
		return nil, errors.New("decryption failed (likely incorrect password or corrupted data)")
	}

	return plaintext, nil
}
