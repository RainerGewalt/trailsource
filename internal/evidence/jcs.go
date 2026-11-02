package evidence

import (
	"encoding/json"

	"github.com/gowebpki/jcs"
)

// CanonicalJCS serialises v per RFC 8785 (JSON Canonicalization Scheme) using
// github.com/gowebpki/jcs, the same library the trail-evidence reference
// implementation uses: object members sorted by UTF-16 code units, minimal
// string escaping and ES6 number formatting.
//
// ES6 number semantics mean every JSON number is an IEEE-754 double: an
// integer literal outside +/-2^53 is rounded (9007199254740993 becomes
// 9007199254740992). Producers therefore transport 64-bit integers and
// rounding-sensitive decimals as strings; see docs/RECORD_MAPPING.md.
func CanonicalJCS(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return CanonicalizeJSON(raw)
}

// CanonicalizeJSON canonicalises already-encoded JSON bytes.
func CanonicalizeJSON(raw []byte) ([]byte, error) {
	return jcs.Transform(raw)
}
