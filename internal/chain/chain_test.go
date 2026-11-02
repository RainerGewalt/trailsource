package chain

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/RainerGewalt/trailsource/internal/evidence"
)

func newRecord(i int) *evidence.Record {
	return &evidence.Record{
		EventType: "test.value.observed",
		Actor:     evidence.Actor{Auth: evidence.ActorAuthSelfDeclared, ID: "t"},
		Policy:    evidence.NoPolicy(),
		Payload:   map[string]any{"i": i},
	}
}

func TestSeqPersistsAcrossRestart(t *testing.T) {
	store := FileStore{Path: filepath.Join(t.TempDir(), "state", "stream.json")}
	producer := evidence.Producer{Kind: evidence.ProducerSourceBridge, ID: "p", Version: "test"}

	s1, err := New(store, producer, "s")
	if err != nil {
		t.Fatal(err)
	}
	var last *evidence.Record
	for i := 0; i < 3; i++ {
		r := newRecord(i)
		if err := s1.Seal(r); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if !r.ChainRestart || r.AnchorHash != evidence.GenesisHash || r.PrevHash != evidence.GenesisHash || r.Seq != 0 {
				t.Fatalf("first record of a fresh stream wrong: %+v", r)
			}
		} else {
			if r.ChainRestart || r.PrevHash != last.ChainHash || r.Seq != last.Seq+1 {
				t.Fatalf("record %d not linked: %+v", i, r)
			}
		}
		last = r
	}

	// Process restart: new Sealer on the same store.
	s2, err := New(store, producer, "s")
	if err != nil {
		t.Fatal(err)
	}
	r := newRecord(3)
	if err := s2.Seal(r); err != nil {
		t.Fatal(err)
	}
	if r.Seq != 3 {
		t.Fatalf("seq must continue after restart, got %d", r.Seq)
	}
	if !r.ChainRestart || r.AnchorHash != last.ChainHash || r.PrevHash != last.ChainHash {
		t.Fatalf("restart record must anchor to the last persisted chain_hash: %+v", r)
	}
	r2 := newRecord(4)
	_ = s2.Seal(r2)
	if r2.ChainRestart || r2.Seq != 4 || r2.PrevHash != r.ChainHash {
		t.Fatalf("second record after restart wrong: %+v", r2)
	}
}

func TestRejectsForeignStreamState(t *testing.T) {
	store := &MemoryStore{state: State{StreamID: "other", Sealed: true}, has: true}
	_, err := New(store, evidence.Producer{Kind: "source-bridge", ID: "p", Version: "t"}, "mine")
	if err == nil || !strings.Contains(err.Error(), "other") {
		t.Fatalf("expected foreign-stream error, got %v", err)
	}
}
