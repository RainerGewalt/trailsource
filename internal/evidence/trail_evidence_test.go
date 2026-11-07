package evidence

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// trailEvidenceDir is the trail-evidence checkout the conformance tests read:
// TRAIL_EVIDENCE_DIR, or the repository next to this one. CI checks out the
// release this adapter claims compatibility with and sets the variable; then
// a missing file is an error, not a skip.
func trailEvidenceDir(t *testing.T) (string, bool) {
	t.Helper()
	if d := os.Getenv("TRAIL_EVIDENCE_DIR"); d != "" {
		return d, true
	}
	return filepath.Join("..", "..", "..", "trail-evidence"), false
}

type referenceVector struct {
	Name                string          `json:"name"`
	Record              json.RawMessage `json:"record"`
	ExpectedPayloadJCS  string          `json:"expected_payload_jcs"`
	ExpectedContentHash string          `json:"expected_content_hash"`
	ExpectedCoreJCS     string          `json:"expected_core_jcs"`
	ExpectedChainHash   string          `json:"expected_chain_hash"`
	ExpectedValid       bool            `json:"expected_valid"`
}

// Members of trail.evidence/1 that TrailSource does not produce. A vector
// that carries one is outside its producer role; the test names it instead
// of dropping the member silently and computing a different chain hash.
var notProduced = []string{"profile", "signature"}

// TestReferenceVectors checks conformance against the reference vectors of
// trail-evidence: for every valid vector within TrailSource's producer role,
// payload JCS, content_hash, core JCS and chain_hash must match byte for
// byte; a valid record with a legacy integer seq must verify as written; every
// invalid vector must fail TrailSource's own verifier.
func TestReferenceVectors(t *testing.T) {
	dir, required := trailEvidenceDir(t)
	raw, err := os.ReadFile(filepath.Join(dir, "testdata", "vectors.json"))
	if err != nil {
		if required {
			t.Fatalf("TRAIL_EVIDENCE_DIR is set, but: %v", err)
		}
		t.Skip("trail-evidence not checked out next to this repository; set TRAIL_EVIDENCE_DIR")
	}
	var file struct {
		Vectors []referenceVector `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Vectors) < 10 {
		t.Fatalf("expected the reference vectors, got %d", len(file.Vectors))
	}
	produced, outside, legacy := 0, 0, 0
	for _, v := range file.Vectors {
		var members map[string]json.RawMessage
		if err := json.Unmarshal(v.Record, &members); err != nil {
			t.Fatalf("%s: %v", v.Name, err)
		}
		if !v.ExpectedValid {
			var line bytes.Buffer
			if err := json.Compact(&line, v.Record); err != nil {
				t.Fatalf("%s: %v", v.Name, err)
			}
			rep, err := VerifyJournal(strings.NewReader(line.String() + "\n"))
			if err == nil && rep.OK() && len(rep.Issues) == 0 {
				t.Errorf("%s: the reference marks this record invalid, TrailSource's verifier finds nothing", v.Name)
			}
			continue
		}
		if seq := bytes.TrimSpace(members["seq"]); len(seq) > 0 && seq[0] != '"' {
			// A legacy integer seq: a producer writes the decimal string, so
			// re-sealing cannot reproduce this hash. The vector is about
			// reading such a record, and TrailSource's verifier must accept
			// it as written.
			var line bytes.Buffer
			if err := json.Compact(&line, v.Record); err != nil {
				t.Fatalf("%s: %v", v.Name, err)
			}
			rep, err := VerifyJournal(strings.NewReader(line.String() + "\n"))
			if err != nil || !rep.OK() {
				t.Errorf("%s: a legacy integer seq must verify as written: %v %+v", v.Name, err, rep.Issues)
			}
			legacy++
			continue
		}
		skip := ""
		for _, m := range notProduced {
			if _, ok := members[m]; ok {
				skip = m
			}
		}
		if skip != "" {
			outside++
			t.Logf("%s: carries %q, which TrailSource does not produce", v.Name, skip)
			continue
		}
		produced++
		payloadJCS, err := CanonicalizeJSON(members["payload"])
		if err != nil {
			t.Fatalf("%s: %v", v.Name, err)
		}
		if string(payloadJCS) != v.ExpectedPayloadJCS {
			t.Errorf("%s: payload JCS\n got %s\nwant %s", v.Name, payloadJCS, v.ExpectedPayloadJCS)
		}
		var rec Record
		dec := json.NewDecoder(bytes.NewReader(v.Record))
		dec.UseNumber() // keep large integers in the payload exact
		if err := dec.Decode(&rec); err != nil {
			t.Fatalf("%s: decode: %v", v.Name, err)
		}
		rec.ContentHash, rec.ChainHash = "", ""
		if err := Seal(&rec); err != nil {
			t.Fatalf("%s: seal: %v", v.Name, err)
		}
		if rec.ContentHash != v.ExpectedContentHash {
			t.Errorf("%s: content_hash %s want %s", v.Name, rec.ContentHash, v.ExpectedContentHash)
		}
		core, err := CoreJCS(&rec)
		if err != nil {
			t.Fatalf("%s: %v", v.Name, err)
		}
		if string(core) != v.ExpectedCoreJCS {
			t.Errorf("%s: core JCS\n got %s\nwant %s", v.Name, core, v.ExpectedCoreJCS)
		}
		if rec.ChainHash != v.ExpectedChainHash {
			t.Errorf("%s: chain_hash %s want %s", v.Name, rec.ChainHash, v.ExpectedChainHash)
		}
	}
	t.Logf("%d valid vectors reproduced, %d legacy read as written, %d outside the producer role", produced, legacy, outside)
	if produced < 5 {
		t.Fatalf("only %d vectors within the producer role; the reference file changed", produced)
	}
}
