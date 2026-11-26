// Package hub talks to the NetKey cloud hub, the same way a box does (netkey/hubclient.py).
package hub

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"math/big"
	"strings"
)

// Invite codes: KEY and 8 groups of 4 (Crockford base32): a 40-bit party id at the hub, then a 120-bit SHA-256
// fingerprint of the host's WireGuard key, so the hub can't hand a member a key of its own.
const (
	alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	prefix   = "KEY"
	idChars  = 8
	fpChars  = 24
)

// IsCode reports whether s looks like a hub invite code (KEY-...).
func IsCode(s string) bool {
	return strings.HasPrefix(alnumUpper(s), prefix)
}

func alnumUpper(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ParseCode returns the code's party id and host-key fingerprint, normalised (O reads as 0, I and L as 1).
func ParseCode(code string) (id, fp string, err error) {
	text := alnumUpper(code)
	if !strings.HasPrefix(text, prefix) || len(text) != len(prefix)+idChars+fpChars {
		return "", "", errors.New("not a complete LAN party code (KEY- and 8 groups of 4 characters)")
	}
	var b strings.Builder
	for _, r := range text[len(prefix):] {
		switch r {
		case 'O':
			r = '0'
		case 'I', 'L':
			r = '1'
		}
		if !strings.ContainsRune(alphabet, r) {
			return "", "", errors.New("the code contains invalid characters")
		}
		b.WriteRune(r)
	}
	body := b.String()
	return body[:idChars], body[idChars:], nil
}

// KeyFingerprint is the first chars base32 characters of SHA-256(raw key) (chars <= 24), like the box's
// hubclient.key_fingerprint.
func KeyFingerprint(pub string, chars int) (string, error) {
	raw, err := base64.StdEncoding.Strict().DecodeString(pub)
	if err != nil || len(raw) != 32 {
		return "", errors.New("bad WireGuard key")
	}
	sum := sha256.Sum256(raw)
	v := new(big.Int).SetBytes(sum[:15])
	v.Rsh(v, uint(5*(fpChars-chars)))
	out := make([]byte, chars)
	mask := big.NewInt(31)
	for i := chars - 1; i >= 0; i-- {
		out[i] = alphabet[new(big.Int).And(v, mask).Int64()]
		v.Rsh(v, 5)
	}
	return string(out), nil
}

// VerifyWords is the short key check a host sees next to a join request (60 bits, XXXX-XXXX-XXXX).
func VerifyWords(pub string) string {
	fp, err := KeyFingerprint(pub, 12)
	if err != nil {
		return ""
	}
	return fp[:4] + "-" + fp[4:8] + "-" + fp[8:]
}
