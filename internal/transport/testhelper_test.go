package transport

import (
	"crypto/ed25519"
	"crypto/rand"
)

// generateTestEd25519Key creates a fresh ed25519 key pair for use in tests.
func generateTestEd25519Key() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}
