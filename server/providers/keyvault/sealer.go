// Package keyvault seals connected-account tokens with envelope encryption
// under an Azure Key Vault key. Each sealed value gets its own random
// AES-256 data key, which Key Vault wraps with an RSA key that never leaves
// the vault, so a copy of the database and the server's configuration
// together still open nothing. The server's identity needs the Key Vault
// Crypto User role (wrap and unwrap) on the key.
package keyvault

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azkeys"
	hex "github.com/crazycatviking/hex/server"
)

const (
	envelopeVersion = 1
	dataKeyBytes    = 32
	// Unwrapped data keys are reused briefly, so a busy connection does not
	// cost a Key Vault call per request.
	dataKeyCacheTTL     = 10 * time.Minute
	maxCachedDataKeys   = 4096
	wrappingAlgorithm   = azkeys.EncryptionAlgorithmRSAOAEP256
	keyOperationTimeout = 15 * time.Second
)

// keyWrapper is the part of azkeys.Client the sealer uses.
type keyWrapper interface {
	WrapKey(ctx context.Context, name, version string, parameters azkeys.KeyOperationParameters, options *azkeys.WrapKeyOptions) (azkeys.WrapKeyResponse, error)
	UnwrapKey(ctx context.Context, name, version string, parameters azkeys.KeyOperationParameters, options *azkeys.UnwrapKeyOptions) (azkeys.UnwrapKeyResponse, error)
}

// Sealer implements hex.CredentialSealer with a Key Vault key. New values
// use the key's current version and record it, so rotating the key in Key
// Vault keeps older values readable as long as their version is enabled.
type Sealer struct {
	keys    keyWrapper
	keyName string

	mu    sync.Mutex
	cache map[[sha256.Size]byte]cachedDataKey
}

type cachedDataKey struct {
	key     []byte
	expires time.Time
}

// envelope is the stored form of a sealed value.
type envelope struct {
	Version    int    `json:"v"`
	KeyVersion string `json:"k"`
	WrappedKey []byte `json:"w"`
	Data       []byte `json:"d"`
}

var _ hex.CredentialSealer = (*Sealer)(nil)

// New uses the RSA key keyName in the vault at vaultURL, for example
// https://hex-kv.vault.azure.net/.
func New(vaultURL, keyName string, credential azcore.TokenCredential) (*Sealer, error) {
	if keyName == "" {
		return nil, errors.New("a Key Vault key name is required")
	}
	client, err := azkeys.NewClient(vaultURL, credential, nil)
	if err != nil {
		return nil, fmt.Errorf("create Key Vault client: %w", err)
	}
	return newSealer(client, keyName), nil
}

func newSealer(keys keyWrapper, keyName string) *Sealer {
	return &Sealer{keys: keys, keyName: keyName, cache: make(map[[sha256.Size]byte]cachedDataKey)}
}

func (s *Sealer) Seal(ctx context.Context, plaintext, associated []byte) ([]byte, error) {
	dataKey := make([]byte, dataKeyBytes)
	if _, err := rand.Read(dataKey); err != nil {
		return nil, fmt.Errorf("generate data key: %w", err)
	}
	aead, err := newAEAD(dataKey)
	if err != nil {
		return nil, err
	}
	data, err := hex.SealAESGCM(aead, plaintext, associated)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, keyOperationTimeout)
	defer cancel()
	algorithm := wrappingAlgorithm
	wrapped, err := s.keys.WrapKey(ctx, s.keyName, "", azkeys.KeyOperationParameters{Algorithm: &algorithm, Value: dataKey}, nil)
	if err != nil {
		return nil, fmt.Errorf("wrap data key in Key Vault: %w", err)
	}
	if wrapped.KID == nil || len(wrapped.Result) == 0 {
		return nil, errors.New("Key Vault returned an incomplete wrapped key")
	}

	sealed := envelope{Version: envelopeVersion, KeyVersion: wrapped.KID.Version(), WrappedKey: wrapped.Result, Data: data}
	s.remember(sealed.WrappedKey, dataKey)
	return json.Marshal(sealed)
}

func (s *Sealer) Open(ctx context.Context, sealed, associated []byte) ([]byte, error) {
	var value envelope
	if err := json.Unmarshal(sealed, &value); err != nil {
		return nil, errors.New("the sealed value is not a Key Vault envelope")
	}
	if value.Version != envelopeVersion {
		return nil, fmt.Errorf("unsupported envelope version %d", value.Version)
	}
	dataKey, err := s.dataKey(ctx, value)
	if err != nil {
		return nil, err
	}
	aead, err := newAEAD(dataKey)
	if err != nil {
		return nil, err
	}
	return hex.OpenAESGCM(aead, value.Data, associated)
}

func (s *Sealer) dataKey(ctx context.Context, value envelope) ([]byte, error) {
	if key, found := s.cached(value.WrappedKey); found {
		return key, nil
	}
	ctx, cancel := context.WithTimeout(ctx, keyOperationTimeout)
	defer cancel()
	algorithm := wrappingAlgorithm
	unwrapped, err := s.keys.UnwrapKey(ctx, s.keyName, value.KeyVersion,
		azkeys.KeyOperationParameters{Algorithm: &algorithm, Value: value.WrappedKey}, nil)
	if err != nil {
		return nil, fmt.Errorf("unwrap data key in Key Vault: %w", err)
	}
	if len(unwrapped.Result) != dataKeyBytes {
		return nil, errors.New("Key Vault returned a data key of the wrong size")
	}
	s.remember(value.WrappedKey, unwrapped.Result)
	return unwrapped.Result, nil
}

func (s *Sealer) cached(wrapped []byte) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, found := s.cache[sha256.Sum256(wrapped)]
	if !found || time.Now().After(entry.expires) {
		return nil, false
	}
	return entry.key, true
}

func (s *Sealer) remember(wrapped, dataKey []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if len(s.cache) >= maxCachedDataKeys {
		for digest, entry := range s.cache {
			if now.After(entry.expires) || len(s.cache) >= maxCachedDataKeys {
				delete(s.cache, digest)
			}
		}
	}
	s.cache[sha256.Sum256(wrapped)] = cachedDataKey{key: dataKey, expires: now.Add(dataKeyCacheTTL)}
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
