// Package crypto implements envelope encryption for sensitive fields.
//
// A Key Encryption Key (KEK) is derived from the operator-supplied master
// passphrase using scrypt. Each Sealed value carries:
//
//   - a random 16-byte salt used to derive its own Data Encryption Key (DEK)
//   - the AES-GCM nonce
//   - the AES-GCM ciphertext
//
// The DEK is derived per-record via scrypt(masterPassphrase, recordSalt) so
// every encrypted blob is independent and self-contained. This is simple,
// dependency-free, and good enough for Phase 1; a future change can swap
// the KEK source to a cloud KMS or HashiCorp Vault without changing the
// on-disk format (only how the DEK is wrapped).
//
// On-disk format (base64-url encoded, no padding):
//
//	"v1." + base64(salt[16] || nonce[12] || ciphertext)
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/scrypt"
)

const (
	saltLen  = 16
	nonceLen = 12
	scryptN  = 1 << 15
	scryptR  = 8
	scryptP  = 1
	keyLen   = 32 // AES-256

	prefix = "v1."
)

// Sealer encrypts and decrypts values using a master passphrase.
type Sealer struct {
	master []byte
}

// NewSealer returns a Sealer for the given passphrase. The passphrase is
// kept in memory; never log it.
func NewSealer(passphrase string) (*Sealer, error) {
	if strings.TrimSpace(passphrase) == "" {
		return nil, errors.New("crypto: empty passphrase")
	}
	return &Sealer{master: []byte(passphrase)}, nil
}

// Seal encrypts plaintext and returns the base64 envelope.
func (s *Sealer) Seal(plaintext []byte) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("crypto: salt: %w", err)
	}
	dek, err := scrypt.Key(s.master, salt, scryptN, scryptR, scryptP, keyLen)
	if err != nil {
		return "", fmt.Errorf("crypto: derive: %w", err)
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, nonceLen)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("crypto: nonce: %w", err)
	}
	ct := gcm.Seal(nil, nonce, plaintext, nil)

	out := make([]byte, 0, saltLen+nonceLen+len(ct))
	out = append(out, salt...)
	out = append(out, nonce...)
	out = append(out, ct...)
	return prefix + base64.RawURLEncoding.EncodeToString(out), nil
}

// Open decrypts an envelope previously produced by Seal.
func (s *Sealer) Open(envelope string) ([]byte, error) {
	if !strings.HasPrefix(envelope, prefix) {
		return nil, errors.New("crypto: unknown envelope version")
	}
	raw, err := base64.RawURLEncoding.DecodeString(envelope[len(prefix):])
	if err != nil {
		return nil, fmt.Errorf("crypto: decode: %w", err)
	}
	if len(raw) < saltLen+nonceLen+16 {
		return nil, errors.New("crypto: envelope too short")
	}
	salt := raw[:saltLen]
	nonce := raw[saltLen : saltLen+nonceLen]
	ct := raw[saltLen+nonceLen:]

	dek, err := scrypt.Key(s.master, salt, scryptN, scryptR, scryptP, keyLen)
	if err != nil {
		return nil, fmt.Errorf("crypto: derive: %w", err)
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, errors.New("crypto: decryption failed")
	}
	return pt, nil
}
