package token

import (
	"crypto/rand"
	"encoding/base64"
)

func New32() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
