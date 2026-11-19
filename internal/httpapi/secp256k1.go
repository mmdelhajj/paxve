package httpapi

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/asn1"
	"errors"
	"math/big"
)

// secp256k1 ECDSA (the curve ZATCA requires for e-invoice keys). Go's crypto/elliptic only has the NIST curves
// (a = -3), so the arithmetic is done here with math/big (affine coordinates; signing a few invoices a minute needs no
// more). Nonces are deterministic (RFC 6979), so a weak random source cannot leak the key.

var (
	k1P, _  = new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEFFFFFC2F", 16)
	k1N, _  = new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141", 16)
	k1Gx, _ = new(big.Int).SetString("79BE667EF9DCBBAC55A06295CE870B07029BFCDB2DCE28D959F2815B16F81798", 16)
	k1Gy, _ = new(big.Int).SetString("483ADA7726A3C4655DA4FBFC0E1108A8FD17B448A68554199C47D08FFB10D4B8", 16)
	k1B     = big.NewInt(7)
)

type k1Point struct{ x, y *big.Int } // x == nil: the point at infinity

func k1Add(a, b k1Point) k1Point {
	if a.x == nil {
		return b
	}
	if b.x == nil {
		return a
	}
	p := k1P
	var l *big.Int
	if a.x.Cmp(b.x) == 0 {
		if new(big.Int).Mod(new(big.Int).Add(a.y, b.y), p).Sign() == 0 {
			return k1Point{}
		}
		// l = 3x² / 2y
		num := new(big.Int).Mul(big.NewInt(3), new(big.Int).Mul(a.x, a.x))
		den := new(big.Int).ModInverse(new(big.Int).Mul(big.NewInt(2), a.y), p)
		l = new(big.Int).Mod(new(big.Int).Mul(num, den), p)
	} else {
		num := new(big.Int).Sub(b.y, a.y)
		den := new(big.Int).ModInverse(new(big.Int).Mod(new(big.Int).Sub(b.x, a.x), p), p)
		l = new(big.Int).Mod(new(big.Int).Mul(num, den), p)
	}
	x := new(big.Int).Mod(new(big.Int).Sub(new(big.Int).Sub(new(big.Int).Mul(l, l), a.x), b.x), p)
	y := new(big.Int).Mod(new(big.Int).Sub(new(big.Int).Mul(l, new(big.Int).Sub(a.x, x)), a.y), p)
	return k1Point{x, y}
}

func k1Mul(k *big.Int, pt k1Point) k1Point {
	r := k1Point{}
	for i := k.BitLen() - 1; i >= 0; i-- {
		r = k1Add(r, r)
		if k.Bit(i) == 1 {
			r = k1Add(r, pt)
		}
	}
	return r
}

func k1G() k1Point { return k1Point{new(big.Int).Set(k1Gx), new(big.Int).Set(k1Gy)} }

func k1OnCurve(pt k1Point) bool {
	if pt.x == nil {
		return false
	}
	l := new(big.Int).Mod(new(big.Int).Mul(pt.y, pt.y), k1P)
	r := new(big.Int).Mod(new(big.Int).Add(new(big.Int).Exp(pt.x, big.NewInt(3), k1P), k1B), k1P)
	return l.Cmp(r) == 0
}

type k1Key struct {
	d   *big.Int
	pub k1Point
}

func k1Generate() (*k1Key, error) {
	for {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		d := new(big.Int).SetBytes(b)
		if d.Sign() > 0 && d.Cmp(k1N) < 0 {
			return &k1Key{d: d, pub: k1Mul(d, k1G())}, nil
		}
	}
}

func k1FromD(d *big.Int) (*k1Key, error) {
	if d == nil || d.Sign() <= 0 || d.Cmp(k1N) >= 0 {
		return nil, errors.New("secp256k1: invalid private key")
	}
	return &k1Key{d: d, pub: k1Mul(d, k1G())}, nil
}

func pad32(b []byte) []byte {
	if len(b) >= 32 {
		return b[len(b)-32:]
	}
	out := make([]byte, 32)
	copy(out[32-len(b):], b)
	return out
}

// rfc6979 nonce for hash h (SHA-256).
func k1Nonce(d *big.Int, h []byte) *big.Int {
	x := pad32(d.Bytes())
	hh := pad32(new(big.Int).Mod(new(big.Int).SetBytes(h), k1N).Bytes())
	v := make([]byte, 32)
	for i := range v {
		v[i] = 1
	}
	k := make([]byte, 32)
	mac := func(key []byte, parts ...[]byte) []byte {
		m := hmac.New(sha256.New, key)
		for _, p := range parts {
			m.Write(p)
		}
		return m.Sum(nil)
	}
	k = mac(k, v, []byte{0}, x, hh)
	v = mac(k, v)
	k = mac(k, v, []byte{1}, x, hh)
	v = mac(k, v)
	for {
		v = mac(k, v)
		t := new(big.Int).SetBytes(v)
		if t.Sign() > 0 && t.Cmp(k1N) < 0 {
			return t
		}
		k = mac(k, v, []byte{0})
		v = mac(k, v)
	}
}

// sign returns the DER signature (SEQUENCE {r, s}, low-s) of hash.
func (k *k1Key) sign(hash []byte) ([]byte, error) {
	e := new(big.Int).SetBytes(hash)
	for {
		nonce := k1Nonce(k.d, hash)
		pt := k1Mul(nonce, k1G())
		r := new(big.Int).Mod(pt.x, k1N)
		if r.Sign() == 0 {
			hash = append(hash, 0)
			continue
		}
		s := new(big.Int).Mul(new(big.Int).ModInverse(nonce, k1N), new(big.Int).Add(e, new(big.Int).Mul(r, k.d)))
		s.Mod(s, k1N)
		if s.Sign() == 0 {
			hash = append(hash, 0)
			continue
		}
		if s.Cmp(new(big.Int).Rsh(k1N, 1)) > 0 {
			s.Sub(k1N, s)
		}
		return asn1.Marshal(struct{ R, S *big.Int }{r, s})
	}
}

func k1Verify(pub k1Point, hash, der []byte) bool {
	var sig struct{ R, S *big.Int }
	if _, err := asn1.Unmarshal(der, &sig); err != nil || sig.R == nil || sig.S == nil {
		return false
	}
	if sig.R.Sign() <= 0 || sig.S.Sign() <= 0 || sig.R.Cmp(k1N) >= 0 || sig.S.Cmp(k1N) >= 0 || !k1OnCurve(pub) {
		return false
	}
	e := new(big.Int).SetBytes(hash)
	w := new(big.Int).ModInverse(sig.S, k1N)
	u1 := new(big.Int).Mod(new(big.Int).Mul(e, w), k1N)
	u2 := new(big.Int).Mod(new(big.Int).Mul(sig.R, w), k1N)
	pt := k1Add(k1Mul(u1, k1G()), k1Mul(u2, pub))
	if pt.x == nil {
		return false
	}
	return new(big.Int).Mod(pt.x, k1N).Cmp(sig.R) == 0
}

var (
	oidECPublicKey = asn1.ObjectIdentifier{1, 2, 840, 10045, 2, 1}
	oidSecp256k1   = asn1.ObjectIdentifier{1, 3, 132, 0, 10}
	oidECDSASHA256 = asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 2}
)

// uncompressed point 04‖X‖Y
func (k *k1Key) pubBytes() []byte {
	return append(append([]byte{4}, pad32(k.pub.x.Bytes())...), pad32(k.pub.y.Bytes())...)
}

type algID struct {
	Algorithm  asn1.ObjectIdentifier
	Parameters asn1.RawValue `asn1:"optional"`
}

type spki struct {
	Algorithm algID
	PublicKey asn1.BitString
}

// spkiDER: SubjectPublicKeyInfo of the key (what QR tag 8 carries).
func (k *k1Key) spkiDER() ([]byte, error) {
	curve, _ := asn1.Marshal(oidSecp256k1)
	return asn1.Marshal(spki{Algorithm: algID{Algorithm: oidECPublicKey, Parameters: asn1.RawValue{FullBytes: curve}},
		PublicKey: asn1.BitString{Bytes: k.pubBytes(), BitLength: 8 * 65}})
}
