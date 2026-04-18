package crypto

import (
	"bytes"
	"testing"
)

func TestSealRoundtrip(t *testing.T) {
	s, err := NewSealer("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	pt := []byte("super-secret-password-123!")
	env, err := s.Seal(pt)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	got, err := s.Open(env)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(got, pt) {
		t.Fatalf("roundtrip mismatch: %q != %q", got, pt)
	}
}

func TestOpenWithWrongPassphraseFails(t *testing.T) {
	a, _ := NewSealer("right")
	b, _ := NewSealer("wrong")
	env, err := a.Seal([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Open(env); err == nil {
		t.Fatal("expected failure with wrong passphrase")
	}
}

func TestSealUniqueCiphertexts(t *testing.T) {
	s, _ := NewSealer("k")
	a, _ := s.Seal([]byte("same"))
	b, _ := s.Seal([]byte("same"))
	if a == b {
		t.Fatal("same plaintext should produce different envelopes")
	}
}

func TestOpenRejectsGarbage(t *testing.T) {
	s, _ := NewSealer("k")
	if _, err := s.Open("not-an-envelope"); err == nil {
		t.Fatal("expected error")
	}
	if _, err := s.Open("v1.AAAA"); err == nil {
		t.Fatal("expected error")
	}
}

func TestEmptyPassphraseRejected(t *testing.T) {
	if _, err := NewSealer("   "); err == nil {
		t.Fatal("expected error")
	}
}
