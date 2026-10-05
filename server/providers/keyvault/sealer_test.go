package keyvault

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azkeys"
)

// fakeVault wraps with local RSA keys by version, like a Key Vault key.
type fakeVault struct {
	current string
	keys    map[string]*rsa.PrivateKey
	unwraps int
	failing bool
}

func newFakeVault(t *testing.T, versions ...string) *fakeVault {
	t.Helper()
	vault := &fakeVault{keys: make(map[string]*rsa.PrivateKey)}
	for _, version := range versions {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		vault.keys[version] = key
		vault.current = version
	}
	return vault
}

func (v *fakeVault) WrapKey(_ context.Context, name, version string, parameters azkeys.KeyOperationParameters, _ *azkeys.WrapKeyOptions) (azkeys.WrapKeyResponse, error) {
	if version == "" {
		version = v.current
	}
	wrapped, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, &v.keys[version].PublicKey, parameters.Value, nil)
	if err != nil {
		return azkeys.WrapKeyResponse{}, err
	}
	id := azkeys.ID("https://vault.example/keys/" + name + "/" + version)
	return azkeys.WrapKeyResponse{KeyOperationResult: azkeys.KeyOperationResult{KID: &id, Result: wrapped}}, nil
}

func (v *fakeVault) UnwrapKey(_ context.Context, _, version string, parameters azkeys.KeyOperationParameters, _ *azkeys.UnwrapKeyOptions) (azkeys.UnwrapKeyResponse, error) {
	v.unwraps++
	if v.failing {
		return azkeys.UnwrapKeyResponse{}, errors.New("vault unavailable")
	}
	key, found := v.keys[version]
	if !found {
		return azkeys.UnwrapKeyResponse{}, errors.New("unknown key version")
	}
	plain, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, key, parameters.Value, nil)
	if err != nil {
		return azkeys.UnwrapKeyResponse{}, err
	}
	return azkeys.UnwrapKeyResponse{KeyOperationResult: azkeys.KeyOperationResult{Result: plain}}, nil
}

func TestSealerEnvelopes(t *testing.T) {
	ctx := context.Background()
	vault := newFakeVault(t, "v1")
	sealer := newSealer(vault, "credentials")
	binding := []byte("credential\x00alice\x00docs")

	sealed, err := sealer.Seal(ctx, []byte("token"), binding)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("token")) {
		t.Fatal("the plaintext is visible in the sealed value")
	}
	var stored envelope
	if err := json.Unmarshal(sealed, &stored); err != nil || stored.KeyVersion != "v1" {
		t.Fatalf("unexpected envelope %s: %v", sealed, err)
	}

	// A fresh sealer has no cached data keys, as after a restart.
	fresh := newSealer(vault, "credentials")
	opened, err := fresh.Open(ctx, sealed, binding)
	if err != nil || string(opened) != "token" {
		t.Fatalf("could not open: %q %v", opened, err)
	}
	if _, err := fresh.Open(ctx, sealed, binding); err != nil || vault.unwraps != 1 {
		t.Fatalf("the data key was not reused: %d unwraps, %v", vault.unwraps, err)
	}
	if _, err := fresh.Open(ctx, sealed, []byte("credential\x00mallory\x00docs")); err == nil {
		t.Fatal("a value opened under another owner's binding")
	}

	// Rotating the key keeps older values readable through their version.
	rotated := newFakeVault(t, "v1")
	rotated.keys = vault.keys
	extra := newFakeVault(t, "v2")
	rotated.keys["v2"] = extra.keys["v2"]
	rotated.current = "v2"
	afterRotation := newSealer(rotated, "credentials")
	if opened, err := afterRotation.Open(ctx, sealed, binding); err != nil || string(opened) != "token" {
		t.Fatalf("an older value did not open after rotation: %v", err)
	}
	resealed, err := afterRotation.Seal(ctx, []byte("token"), binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(resealed, &stored); err != nil || stored.KeyVersion != "v2" {
		t.Fatalf("a new value did not use the current key version: %s", resealed)
	}

	unavailable := newSealer(&fakeVault{keys: vault.keys, failing: true}, "credentials")
	if _, err := unavailable.Open(ctx, sealed, binding); err == nil {
		t.Fatal("opened without Key Vault")
	}
	if _, err := sealer.Open(ctx, []byte("not an envelope"), binding); err == nil {
		t.Fatal("opened a value that is not an envelope")
	}
}
