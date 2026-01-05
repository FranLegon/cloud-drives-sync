package crypto

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateAndSaveSalt(t *testing.T) {
	// Create temp file
	tmpFile := filepath.Join(t.TempDir(), "test_salt")
	defer os.Remove(tmpFile)

	salt, err := GenerateAndSaveSalt(tmpFile)
	if err != nil {
		t.Fatalf("Failed to generate salt: %v", err)
	}

	if len(salt) != saltSize {
		t.Errorf("Expected salt size %d, got %d", saltSize, len(salt))
	}

	// Verify it was saved
	loadedSalt, err := LoadSalt(tmpFile)
	if err != nil {
		t.Fatalf("Failed to load salt: %v", err)
	}

	if string(salt) != string(loadedSalt) {
		t.Error("Loaded salt doesn't match generated salt")
	}
}

func TestDeriveKey(t *testing.T) {
	password := "test-password"
	salt := make([]byte, saltSize)

	key := DeriveKey(password, salt)
	if len(key) != keySize {
		t.Errorf("Expected key size %d, got %d", keySize, len(key))
	}

	// Same password and salt should produce same key
	key2 := DeriveKey(password, salt)
	if string(key) != string(key2) {
		t.Error("Same password/salt produced different keys")
	}

	// Different password should produce different key
	key3 := DeriveKey("different-password", salt)
	if string(key) == string(key3) {
		t.Error("Different passwords produced same key")
	}
}

func TestEncryptDecrypt(t *testing.T) {
	password := "test-password"
	salt := make([]byte, saltSize)
	key := DeriveKey(password, salt)

	plaintext := []byte("Hello, World! This is a test message.")

	// Encrypt
	ciphertext, err := Encrypt(plaintext, key)
	if err != nil {
		t.Fatalf("Encryption failed: %v", err)
	}

	// Decrypt
	decrypted, err := Decrypt(ciphertext, key)
	if err != nil {
		t.Fatalf("Decryption failed: %v", err)
	}

	if string(plaintext) != string(decrypted) {
		t.Error("Decrypted text doesn't match original")
	}
}

func TestHashBytes(t *testing.T) {
	data := []byte("test data")
	hash1 := HashBytes(data)

	if hash1 == "" {
		t.Error("Hash is empty")
	}

	// Same data should produce same hash
	hash2 := HashBytes(data)
	if hash1 != hash2 {
		t.Error("Same data produced different hashes")
	}

	// Different data should produce different hash
	hash3 := HashBytes([]byte("different data"))
	if hash1 == hash3 {
		t.Error("Different data produced same hash")
	}
}
