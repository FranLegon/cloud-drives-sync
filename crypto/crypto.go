package crypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"

	"golang.org/x/crypto/argon2"
)

const (
	saltLen      = 16
	nonceLen     = 12
	keyLen       = 32
	argonTime    = 3
	argonMem     = 64 * 1024
	argonThreads = 4
)

func deriveKey(password string, salt []byte) []byte {
	return argon2.IDKey([]byte(password), salt, argonTime, argonMem, argonThreads, keyLen)
}

func Encrypt(plaintext []byte, password string) ([]byte, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	key := deriveKey(password, salt)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, nonceLen)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ciphertext := aesgcm.Seal(nil, nonce, plaintext, nil)
	buf := bytes.Buffer{}
	buf.Write(salt)
	buf.Write(nonce)
	binary.Write(&buf, binary.BigEndian, uint32(len(ciphertext)))
	buf.Write(ciphertext)
	return buf.Bytes(), nil
}

func Decrypt(data []byte, password string) ([]byte, error) {
	if len(data) < saltLen+nonceLen+4 {
		return nil, errors.New("data too short")
	}
	salt := data[:saltLen]
	nonce := data[saltLen : saltLen+nonceLen]
	clen := binary.BigEndian.Uint32(data[saltLen+nonceLen : saltLen+nonceLen+4])
	ciphertext := data[saltLen+nonceLen+4:]
	if uint32(len(ciphertext)) != clen {
		return nil, errors.New("ciphertext length mismatch")
	}
	key := deriveKey(password, salt)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return aesgcm.Open(nil, nonce, ciphertext, nil)
}
