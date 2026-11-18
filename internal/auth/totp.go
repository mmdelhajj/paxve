package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP (RFC 6238): 30-second steps, 6 digits, HMAC-SHA1 — what every authenticator app supports.

const (
	totpStep   = 30
	totpDigits = 6
)

var totpEnc = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a random 160-bit secret, base32 encoded (the form authenticator apps take).
func NewTOTPSecret() string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return totpEnc.EncodeToString(b)
}

// TOTPURI is the otpauth:// link shown as a QR code.
func TOTPURI(secret, issuer, account string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", "6")
	q.Set("period", "30")
	return "otpauth://totp/" + label + "?" + q.Encode()
}

func totpCode(key []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	m := hmac.New(sha1.New, key)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", v%1000000)
}

// TOTPCodeAt returns the code for time t (used by tests).
func TOTPCodeAt(secret string, t time.Time) string {
	key, err := totpEnc.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return ""
	}
	return totpCode(key, t.Unix()/totpStep)
}

// VerifyTOTP checks code against the current step ±1 (clock drift). It returns the matched step, which
// the caller stores so the same code cannot be used twice (steps <= lastStep are refused).
func VerifyTOTP(secret, code string, now time.Time, lastStep int64) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != totpDigits {
		return 0, false
	}
	key, err := totpEnc.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil || len(key) == 0 {
		return 0, false
	}
	cur := now.Unix() / totpStep
	for _, s := range []int64{cur - 1, cur, cur + 1} {
		if s <= lastStep {
			continue
		}
		if hmac.Equal([]byte(totpCode(key, s)), []byte(code)) {
			return s, true
		}
	}
	return 0, false
}

// NewRecoveryCodes returns n one-time codes like "k7m2-9xqp-c4hd" and their hashes (stored).
func NewRecoveryCodes(n int) (codes, hashes []string) {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	for range n {
		b := make([]byte, 12)
		if _, err := rand.Read(b); err != nil {
			panic(err)
		}
		var sb strings.Builder
		for i, x := range b {
			if i > 0 && i%4 == 0 {
				sb.WriteByte('-')
			}
			sb.WriteByte(alphabet[int(x)%len(alphabet)])
		}
		c := sb.String()
		codes = append(codes, c)
		hashes = append(hashes, HashRecoveryCode(c))
	}
	return codes, hashes
}

// HashRecoveryCode normalises (case, dashes, spaces) and hashes a recovery code.
func HashRecoveryCode(c string) string {
	c = strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(c)))
	h := sha256.Sum256([]byte("paxve-recovery:" + c))
	return hex.EncodeToString(h[:])
}
