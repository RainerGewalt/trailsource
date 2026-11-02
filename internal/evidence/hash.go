package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// HashPrefix is the prefix of every hash string in a record.
const HashPrefix = "sha256:"

// GenesisHash is the prev_hash of the first record of a stream and the
// anchor_hash of a restart whose predecessor is unknown.
const GenesisHash = HashPrefix + "0000000000000000000000000000000000000000000000000000000000000000"

// Hash returns "sha256:" + hex of SHA-256 over the RFC 8785 canonical encoding of v.
func Hash(v any) (string, error) {
	b, err := CanonicalJCS(v)
	if err != nil {
		return "", err
	}
	return Sum(b), nil
}

// HashJSON canonicalises already-encoded JSON bytes and hashes them.
func HashJSON(raw []byte) (string, error) {
	b, err := CanonicalizeJSON(raw)
	if err != nil {
		return "", err
	}
	return Sum(b), nil
}

// Sum returns "sha256:" + lowercase hex of SHA-256 over b.
func Sum(b []byte) string {
	h := sha256.Sum256(b)
	return HashPrefix + hex.EncodeToString(h[:])
}

// IsHash reports whether s has the shape "sha256:" + 64 lowercase hex characters.
func IsHash(s string) bool {
	if !strings.HasPrefix(s, HashPrefix) || len(s) != len(HashPrefix)+64 {
		return false
	}
	for _, c := range s[len(HashPrefix):] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
