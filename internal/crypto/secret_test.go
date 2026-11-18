package crypto

import (
	"strings"
	"testing"
)

const testKey = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

func TestSealOpen(t *testing.T) {
	b, err := NewBox(testKey)
	if err != nil {
		t.Fatal(err)
	}
	s, err := b.Seal("my-token-1234")
	if err != nil {
		t.Fatal(err)
	}
	if !IsSealed(s) || strings.Contains(s, "my-token") || !strings.HasPrefix(s, "enc:v1:") {
		t.Fatalf("not sealed: %q", s)
	}
	s2, _ := b.Seal("my-token-1234")
	if s == s2 {
		t.Fatal("same ciphertext twice: nonce not random")
	}
	if again, _ := b.Seal(s); again != s {
		t.Fatal("sealing a sealed value must be a no-op")
	}
	p, err := b.Open(s)
	if err != nil || p != "my-token-1234" {
		t.Fatalf("open = %q, %v", p, err)
	}
	if p, err := b.Open("plain-legacy"); err != nil || p != "plain-legacy" {
		t.Fatalf("legacy plaintext must pass through: %q %v", p, err)
	}
	if e, _ := b.Seal(""); e != "" {
		t.Fatal("empty must stay empty")
	}
}

func TestWrongKeyAndTamper(t *testing.T) {
	a, _ := NewBox(testKey)
	other, _ := NewBox(strings.Repeat("ab", 32))
	s, _ := a.Seal("secret")
	if _, err := other.Open(s); err == nil {
		t.Fatal("opened with the wrong key")
	}
	bad := s[:len(s)-2] + "AA"
	if _, err := a.Open(bad); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	if _, err := a.Open("enc:v9:abc"); err == nil {
		t.Fatal("unknown key version accepted")
	}
}

func TestKeyFormats(t *testing.T) {
	if _, err := NewBox("short"); err == nil {
		t.Fatal("short key accepted")
	}
	if _, err := NewBox("AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="); err != nil {
		t.Fatalf("base64 key rejected: %v", err)
	}
}
