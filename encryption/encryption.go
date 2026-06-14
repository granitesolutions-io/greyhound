package encryption

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// DeriveKey derives a 32-byte AES-256 key from the input string.
// If the input is exactly 64 hex characters, it is decoded directly.
// Otherwise the input is SHA-256 hashed to produce the key.
func DeriveKey(input string) ([]byte, error) {
	if input == "" {
		return nil, errors.New("encryption key must not be empty")
	}

	// If 64 hex chars, decode directly to 32 bytes.
	if len(input) == 64 {
		key, err := hex.DecodeString(input)
		if err == nil && len(key) == 32 {
			return key, nil
		}
	}

	// Otherwise SHA-256 hash the input.
	h := sha256.Sum256([]byte(input))
	return h[:], nil
}

// Encrypt encrypts plaintext using AES-256-GCM with the given key.
// The nonce is prepended to the ciphertext. The result is base64-encoded.
func Encrypt(key, plaintext []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// Decrypt decrypts a base64-encoded ciphertext (with prepended nonce)
// using AES-256-GCM with the given key.
func Decrypt(key []byte, encoded string) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(data) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}

	nonce, ciphertext := data[:nonceSize], data[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}

	return plaintext, nil
}
