package local

import (
	"crypto/rand"
	"encoding/hex"
)

func randomName() (string, error) {
	var randomBytes [16]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return "", err
	}

	return hex.EncodeToString(randomBytes[:]), nil
}
