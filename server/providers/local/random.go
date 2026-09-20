package local

import (
	"crypto/rand"
	"encoding/hex"
)

func randomName() (string, error) {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	return hex.EncodeToString(b), err
}
