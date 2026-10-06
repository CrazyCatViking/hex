package hex

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// CredentialSealer encrypts connected-account tokens before they are
// stored. Associated data binds each sealed value to its owner and
// connector, so a value copied to another record does not open. The
// keyvault provider wraps a fresh key per credential with a Key Vault key
// that never leaves the vault; KeySealer uses a local AES-256 key, for
// development. Changing sealers makes existing connections unreadable;
// people then reconnect.
// OAuth connection state uses this same sealer with distinct associated data;
// all instances serving a deployment must be able to open each other's values.
type CredentialSealer interface {
	Seal(ctx context.Context, plaintext, associated []byte) ([]byte, error)
	Open(ctx context.Context, sealed, associated []byte) ([]byte, error)
}

// ParseCredentialKey decodes a base64 AES-256 key for a KeySealer.
func ParseCredentialKey(value string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return nil, fmt.Errorf("decode credential key: %w", err)
	}
	if len(key) != 32 {
		return nil, errors.New("the credential key must be 32 bytes, base64 encoded")
	}
	return key, nil
}

// KeySealer seals with AES-256-GCM under a key held in process memory.
type KeySealer struct {
	aead cipher.AEAD
}

func NewKeySealer(key []byte) (*KeySealer, error) {
	if len(key) != 32 {
		return nil, errors.New("the credential key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &KeySealer{aead: aead}, nil
}

func (s *KeySealer) Seal(_ context.Context, plaintext, associated []byte) ([]byte, error) {
	return SealAESGCM(s.aead, plaintext, associated)
}

func (s *KeySealer) Open(_ context.Context, sealed, associated []byte) ([]byte, error) {
	return OpenAESGCM(s.aead, sealed, associated)
}

// SealAESGCM prefixes a random nonce to the ciphertext.
func SealAESGCM(aead cipher.AEAD, plaintext, associated []byte) ([]byte, error) {
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plaintext, associated), nil
}

// OpenAESGCM opens a value sealed by SealAESGCM.
func OpenAESGCM(aead cipher.AEAD, sealed, associated []byte) ([]byte, error) {
	if len(sealed) < aead.NonceSize() {
		return nil, errors.New("sealed value is too short")
	}
	nonce, ciphertext := sealed[:aead.NonceSize()], sealed[aead.NonceSize():]
	return aead.Open(nil, nonce, ciphertext, associated)
}
