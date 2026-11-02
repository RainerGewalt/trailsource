package evidence

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func baseRecord() Record {
	return Record{
		Schema:    Schema,
		Producer:  Producer{Kind: ProducerSourceBridge, ID: "vector", Version: "0.0.0"},
		StreamID:  "vec",
		Seq:       0,
		PrevHash:  GenesisHash,
		EventType: "test.vector.one",
		Time:      Time{RecordedAt: "2026-01-01T00:00:00.000Z", Source: TimeSourceSystem},
		Actor:     Actor{Auth: ActorAuthNone},
		Policy:    NoPolicy(),
		Payload:   map[string]any{"b": 1, "a": "x"},
	}
}

func TestSealIsDeterministicAndKeyOrderIndependent(t *testing.T) {
	a := baseRecord()
	b := baseRecord()
	b.Payload = map[string]any{"a": "x", "b": 1}
	if err := Seal(&a); err != nil {
		t.Fatal(err)
	}
	if err := Seal(&b); err != nil {
		t.Fatal(err)
	}
	if a.ContentHash != b.ContentHash || a.ChainHash != b.ChainHash {
		t.Fatalf("hashes differ for equal records: %+v vs %+v", a, b)
	}
	if !IsHash(a.ContentHash) || !IsHash(a.ChainHash) {
		t.Fatalf("hash shape wrong: %s %s", a.ContentHash, a.ChainHash)
	}
	core, _ := CoreJCS(&a)
	if strings.Contains(string(core), "payload") || strings.Contains(string(core), "chain_hash") {
		t.Fatalf("core must not contain payload or chain_hash: %s", core)
	}
}

func TestSealContextIsNotHashed(t *testing.T) {
	a := baseRecord()
	b := baseRecord()
	b.Context = map[string]any{"site": "plant-a"}
	_ = Seal(&a)
	_ = Seal(&b)
	if a.ChainHash != b.ChainHash {
		t.Fatal("context must not influence chain_hash")
	}
}

func TestSealRestartRules(t *testing.T) {
	r := baseRecord()
	r.ChainRestart = true
	if err := Seal(&r); err == nil {
		t.Fatal("chain_restart without anchor_hash must fail")
	}
	r.AnchorHash = GenesisHash
	if err := Seal(&r); err != nil {
		t.Fatal(err)
	}
	r2 := baseRecord()
	r2.AnchorHash = GenesisHash
	if err := Seal(&r2); err == nil {
		t.Fatal("anchor_hash without chain_restart must fail")
	}
}

type vector struct {
	Name                string          `json:"name"`
	Record              json.RawMessage `json:"record"`
	ExpectedContentHash string          `json:"expected_content_hash"`
	ExpectedChainHash   string          `json:"expected_chain_hash"`
	ExpectedCoreJCS     string          `json:"expected_core_jcs"`
}

// TestVectors checks the committed vectors; `go test -update-vectors` rewrites
// them from this implementation (reference role until trail-evidence exists).
func TestVectors(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "vectors.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var vectors []vector
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors) < 3 {
		t.Fatalf("expected at least 3 vectors, got %d", len(vectors))
	}
	var all strings.Builder
	for _, v := range vectors {
		var rec Record
		dec := json.NewDecoder(strings.NewReader(string(v.Record)))
		dec.UseNumber() // keep large integers in the payload exact
		if err := dec.Decode(&rec); err != nil {
			t.Fatalf("%s: %v", v.Name, err)
		}
		rec.ContentHash, rec.ChainHash = "", ""
		if err := Seal(&rec); err != nil {
			t.Fatalf("%s: seal: %v", v.Name, err)
		}
		core, _ := CoreJCS(&rec)
		if rec.ContentHash != v.ExpectedContentHash {
			t.Errorf("%s: content_hash %s want %s", v.Name, rec.ContentHash, v.ExpectedContentHash)
		}
		if rec.ChainHash != v.ExpectedChainHash {
			t.Errorf("%s: chain_hash %s want %s", v.Name, rec.ChainHash, v.ExpectedChainHash)
		}
		if string(core) != v.ExpectedCoreJCS {
			t.Errorf("%s: core jcs\n got %s\nwant %s", v.Name, core, v.ExpectedCoreJCS)
		}
		line, _ := json.Marshal(rec)
		all.Write(line)
		all.WriteByte('\n')
	}
	// The vectors form one valid stream (genesis, successor, restart) and
	// must pass the generic verifier as a journal.
	rep, err := VerifyJournal(strings.NewReader(all.String()))
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK() {
		t.Errorf("verifier rejected vectors: %+v", rep.Issues)
	}
	if len(rep.Streams) != 1 || rep.Streams[0].Restarts != 1 {
		t.Errorf("unexpected stream report %+v", rep.Streams)
	}
}
