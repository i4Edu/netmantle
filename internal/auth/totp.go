package auth

// totp.go implements RFC 6238 Time-based One-Time Passwords using only
// the Go standard library (crypto/hmac, crypto/sha1, encoding/base32).
// No external dependency is required.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // TOTP per RFC 6238 mandates SHA-1
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"time"
)

const totpDigits = 6
const totpPeriod = 30 // seconds per RFC 6238

// GenerateTOTPSecret returns a random 20-byte TOTP secret encoded as
// base32 (without padding), suitable for storage and QR-code generation.
func GenerateTOTPSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: generate totp secret: %w", err)
	}
	return strings.TrimRight(base32.StdEncoding.EncodeToString(b), "="), nil
}

// TOTPOtpauthURL returns the otpauth:// URI used to provision authenticator
// apps (Google Authenticator, Authy, etc.) via QR code.
func TOTPOtpauthURL(issuer, username, secret string) string {
	return fmt.Sprintf("otpauth://totp/%s:%s?secret=%s&issuer=%s&algorithm=SHA1&digits=%d&period=%d",
		issuer, username, secret, issuer, totpDigits, totpPeriod)
}

// VerifyTOTP checks whether code matches the current (or adjacent) TOTP
// window for secret. Adjacent windows (±1 period) are accepted to handle
// clock skew between client and server.
func VerifyTOTP(secret, code string) bool {
	// Normalise base32: upper-case, strip padding and spaces.
	secret = strings.ToUpper(strings.ReplaceAll(secret, " ", ""))
	// Pad to multiple of 8.
	if rem := len(secret) % 8; rem != 0 {
		secret += strings.Repeat("=", 8-rem)
	}
	key, err := base32.StdEncoding.DecodeString(secret)
	if err != nil {
		return false
	}
	now := time.Now().Unix() / int64(totpPeriod)
	for _, counter := range []int64{now - 1, now, now + 1} {
		if totp(key, counter) == code {
			return true
		}
	}
	return false
}

// totp computes a HOTP value for the given key and counter.
func totp(key []byte, counter int64) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(counter))
	mac := hmac.New(sha1.New, key)
	mac.Write(buf[:])
	h := mac.Sum(nil)
	offset := h[len(h)-1] & 0x0f
	code := (int(h[offset]&0x7f)<<24 |
		int(h[offset+1])<<16 |
		int(h[offset+2])<<8 |
		int(h[offset+3])) % int(math.Pow10(totpDigits))
	return fmt.Sprintf("%0*d", totpDigits, code)
}
