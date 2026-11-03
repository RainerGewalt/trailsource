package evidence

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// Seq is the position of a record in its stream. It is written as a
// canonical decimal string: "0", "1", ... without sign, leading zeros or
// exponent, within the uint64 range. A string keeps every value exact in
// every JSON implementation; RFC 8785 would round a JSON number beyond 2^53.
//
// Journals written before this rule carry seq as a JSON integer.
// UnmarshalJSON and ParseSeq accept that form too, and the verifier reports
// it as the notice legacy_integer_seq.
type Seq uint64

// String returns the canonical decimal form.
func (s Seq) String() string { return strconv.FormatUint(uint64(s), 10) }

// MarshalJSON writes the canonical decimal string.
func (s Seq) MarshalJSON() ([]byte, error) {
	return []byte(`"` + s.String() + `"`), nil
}

// UnmarshalJSON accepts the canonical decimal string and, for older
// journals, a non-negative JSON integer.
func (s *Seq) UnmarshalJSON(b []byte) error {
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	if str, isStr := v.(string); isStr {
		n, err := ParseSeqString(str)
		if err != nil {
			return err
		}
		*s = Seq(n)
		return nil
	}
	n, err := strconv.ParseUint(string(b), 10, 64)
	if err != nil {
		return fmt.Errorf("seq %s is neither a canonical decimal string nor a non-negative integer", b)
	}
	*s = Seq(n)
	return nil
}

// ParseSeqString parses a canonical decimal seq string.
func ParseSeqString(str string) (uint64, error) {
	if str == "" || (len(str) > 1 && str[0] == '0') {
		return 0, fmt.Errorf("seq %q is not a canonical decimal string", str)
	}
	for _, c := range str {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("seq %q is not a canonical decimal string", str)
		}
	}
	n, err := strconv.ParseUint(str, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("seq %q is outside the uint64 range", str)
	}
	return n, nil
}

// SeqForm says how a decoded record wrote seq.
type SeqForm int

const (
	SeqInvalid SeqForm = iota // missing, negative, fractional, malformed
	SeqString                 // canonical decimal string (current rule)
	SeqInteger                // JSON integer (older journals)
)

// ParseSeq reads seq from a record decoded with json.Decoder.UseNumber.
func ParseSeq(v any) (uint64, SeqForm) {
	switch t := v.(type) {
	case string:
		n, err := ParseSeqString(t)
		if err != nil {
			return 0, SeqInvalid
		}
		return n, SeqString
	case json.Number:
		n, err := strconv.ParseUint(string(t), 10, 64)
		if err != nil {
			return 0, SeqInvalid
		}
		return n, SeqInteger
	}
	return 0, SeqInvalid
}
