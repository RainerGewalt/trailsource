package opcua

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/RainerGewalt/trailsource/internal/chain"
	"github.com/RainerGewalt/trailsource/internal/config"
	"github.com/RainerGewalt/trailsource/internal/evidence"
	"github.com/RainerGewalt/trailsource/internal/journal"
	"github.com/RainerGewalt/trailsource/internal/mapping"
	"github.com/RainerGewalt/trailsource/internal/report"
)

// Audit E2: failure to append must not advance the persisted chain position.
// Closing the journal injects a deterministic write error without filling a disk.
func TestJournalFailureDoesNotAdvanceChain(t *testing.T) {
	store := &chain.MemoryStore{}
	sealer, err := chain.New(store, evidence.Producer{Kind: "source-bridge", ID: "test", Version: "test"}, "source")
	if err != nil {
		t.Fatal(err)
	}
	j, err := journal.Open(filepath.Join(t.TempDir(), "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	a := &Adapter{sealer: sealer, journal: j}
	r := &evidence.Record{EventType: "source.value.observed", Actor: evidence.Actor{Auth: evidence.ActorAuthSelfDeclared, ID: "test"}, Policy: evidence.NoPolicy(), Payload: map[string]any{"value": 1}}
	if err := a.emit(r, "test/values"); err == nil {
		t.Fatal("closed journal must reject the record")
	}
	if state, found, err := store.Load(); err != nil || found || state.Sealed {
		t.Fatalf("journal append failed but chain state advanced: %+v, found=%v, err=%v", state, found, err)
	}
	if _, _, sealed := sealer.Position(); sealed {
		t.Fatal("in-memory state advanced after failed append")
	}
}

type capturePublisher struct {
	records [][]byte
	err     error
}

func (p *capturePublisher) Publish(_ string, b []byte) error {
	p.records = append(p.records, append([]byte(nil), b...))
	return p.err
}
func (p *capturePublisher) PublishHealth(any) error { return nil }

var testProducer = evidence.Producer{Kind: evidence.ProducerSourceBridge, ID: "test", Version: "test"}

func fixtureAdapter(dir string) *Adapter {
	cfg := &config.Config{}
	cfg.Source.ID, cfg.Producer.ID = "source", "test"
	cfg.StateDir = filepath.Join(dir, "state")
	cfg.Journal.Enabled = true
	cfg.Journal.Path = filepath.Join(dir, "journal.jsonl")
	cfg.Path = "test.yaml"
	cfg.Version, cfg.Revision = evidence.GenesisHash, "1"
	cfg.MQTT.TopicPrefix = "test"
	return &Adapter{cfg: cfg, mapping: mapping.Empty(), version: "test", pub: &capturePublisher{}}
}
func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}
func valueRecord() *evidence.Record {
	return &evidence.Record{EventType: EventValueObserved, Actor: evidence.Actor{Auth: evidence.ActorAuthSelfDeclared, ID: "test"}, Policy: evidence.NoPolicy(), Payload: map[string]any{"value": 1}}
}

// Audit PR-3: deterministic versions of both crash windows, and lost state.
// The negative control appends with the old, unreconciled state and must break.
func TestRestartReconcilesCrashWindowsAndReportsUncertainty(t *testing.T) {
	for _, kind := range []string{"state_ahead_of_journal", "state_behind_journal", "state_missing"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			a := fixtureAdapter(dir)
			if err := a.openEvidence(testProducer); err != nil {
				t.Fatal(err)
			}
			if err := a.emitStart(false, false); err != nil {
				t.Fatal(err)
			}
			statePath := filepath.Join(a.cfg.StateDir, "source.chain.json")
			oldState := readFile(t, statePath)
			if err := a.emit(valueRecord(), "values"); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "state_ahead_of_journal":
				if err := a.sealer.Seal(valueRecord()); err != nil {
					t.Fatal(err)
				} // old state-first window
			case "state_behind_journal":
				writeFile(t, statePath, oldState)
			case "state_missing":
				if err := os.Remove(statePath); err != nil {
					t.Fatal(err)
				}
			}
			if err := a.journal.Close(); err != nil {
				t.Fatal(err)
			}
			before := readFile(t, a.cfg.Journal.Path)
			legacy, err := chain.New(chain.FileStore{Path: statePath}, testProducer, "source")
			if err != nil {
				t.Fatal(err)
			}
			control := valueRecord()
			// In-memory copy avoids changing the persisted mismatch under test.
			state, exists, err := (chain.FileStore{Path: statePath}).Load()
			if err != nil {
				t.Fatal(err)
			}
			mem := &chain.MemoryStore{}
			if exists {
				_ = mem.Save(state)
			}
			legacy, err = chain.New(mem, testProducer, "source")
			if err != nil {
				t.Fatal(err)
			}
			if err := legacy.Seal(control); err != nil {
				t.Fatal(err)
			}
			line, _ := json.Marshal(control)
			broken := append(append([]byte(nil), before...), append(line, '\n')...)
			v, err := evidence.VerifyJournal(bytes.NewReader(broken))
			if err != nil || v.OK() {
				t.Fatal("negative control did not expose unreconciled chain", v, err)
			}

			b := fixtureAdapter(dir)
			if err := b.openEvidence(testProducer); err != nil {
				t.Fatal(err)
			}
			defer b.journal.Close()
			if b.recovery == nil || b.recovery.Reason != kind {
				t.Fatalf("wrong recovery: %+v", b.recovery)
			}
			if !bytes.Equal(readFile(t, b.cfg.Journal.Path), before) {
				t.Fatal("recovery rewrote prior evidence")
			}
			// Reopening before the restart record still finds the mismatch.
			c := fixtureAdapter(dir)
			if err := c.openEvidence(testProducer); err != nil {
				t.Fatal(err)
			}
			if c.recovery == nil {
				t.Fatal("startup erased mismatch before recording it")
			}
			_ = c.journal.Close()
			if err := b.emitStart(true, true); err != nil {
				t.Fatal(err)
			}
			var restart evidence.Record
			if err := json.Unmarshal(b.pub.(*capturePublisher).records[0], &restart); err != nil {
				t.Fatal(err)
			}
			if kind == "state_ahead_of_journal" && uint64(restart.Seq) <= b.recovery.Previous.Seq {
				t.Fatal("recovery reused a possibly published sequence number")
			}
			if err := b.emit(valueRecord(), "values"); err != nil {
				t.Fatal(err)
			}
			all := readFile(t, b.cfg.Journal.Path)
			v, err = evidence.VerifyJournal(bytes.NewReader(all))
			if err != nil || !v.OK() {
				t.Fatal("recovered journal fails verification", v, err)
			}
			if !bytes.Contains(all, []byte(`"reason":"`+kind+`"`)) || !bytes.Contains(all, []byte("completeness cannot be established")) {
				t.Fatal("restart omits observed mismatch/uncertainty")
			}
			r, err := report.Build("test.jsonl", all)
			if err != nil {
				t.Fatal(err)
			}
			var text, html bytes.Buffer
			if err := r.WriteText(&text); err != nil {
				t.Fatal(err)
			}
			if err := r.WriteHTML(&html); err != nil {
				t.Fatal(err)
			}
			for _, rendered := range []string{text.String(), html.String()} {
				if !strings.Contains(rendered, "recovered") || !strings.Contains(rendered, "completeness cannot be established") {
					t.Fatal("report hides recovery boundary")
				}
			}
			if os.Getenv("FG_EVIDENCE_DIR") != "" {
				writeFile(t, filepath.Join(os.Getenv("FG_EVIDENCE_DIR"), kind+".jsonl"), all)
			}
		})
	}
}

func TestRotationAndNormalRestartKeepAnchor(t *testing.T) {
	dir := t.TempDir()
	a := fixtureAdapter(dir)
	if err := a.openEvidence(testProducer); err != nil {
		t.Fatal(err)
	}
	if err := a.emitStart(false, false); err != nil {
		t.Fatal(err)
	}
	if err := a.emit(valueRecord(), "values"); err != nil {
		t.Fatal(err)
	}
	if err := a.journal.Close(); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, a.cfg.Journal.Path)
	if err := os.Rename(a.cfg.Journal.Path, a.cfg.Journal.Path+".old"); err != nil {
		t.Fatal(err)
	}
	b := fixtureAdapter(dir)
	if err := b.openEvidence(testProducer); err != nil {
		t.Fatal(err)
	}
	if b.recovery != nil {
		t.Fatal("rotation reported as recovery")
	}
	if err := b.emitStart(true, true); err != nil {
		t.Fatal(err)
	}
	if err := b.journal.Close(); err != nil {
		t.Fatal(err)
	}
	after := readFile(t, b.cfg.Journal.Path)
	for _, data := range [][]byte{after, append(before, after...)} {
		v, err := evidence.VerifyJournal(bytes.NewReader(data))
		if err != nil || !v.OK() {
			t.Fatal(v, err)
		}
	}
	c := fixtureAdapter(dir)
	if err := c.openEvidence(testProducer); err != nil {
		t.Fatal(err)
	}
	defer c.journal.Close()
	if c.recovery != nil {
		t.Fatal("matched state spuriously recovered")
	}
}

func TestStartupRefusesTamperedJournalOrConflictingState(t *testing.T) {
	for _, kind := range []string{"payload", "same_seq_hash", "foreign_stream", "reordered"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			a := fixtureAdapter(dir)
			if err := a.openEvidence(testProducer); err != nil {
				t.Fatal(err)
			}
			if err := a.emitStart(false, false); err != nil {
				t.Fatal(err)
			}
			if err := a.emit(valueRecord(), "values"); err != nil {
				t.Fatal(err)
			}
			_ = a.journal.Close()
			path := a.cfg.Journal.Path
			switch kind {
			case "payload":
				writeFile(t, path, bytes.Replace(readFile(t, path), []byte(`"value":1`), []byte(`"value":2`), 1))
			case "same_seq_hash":
				store := chain.FileStore{Path: filepath.Join(a.cfg.StateDir, "source.chain.json")}
				state, _, _ := store.Load()
				state.ChainHash = evidence.GenesisHash
				if err := store.Save(state); err != nil {
					t.Fatal(err)
				}
			case "foreign_stream":
				s, _ := chain.New(&chain.MemoryStore{}, testProducer, "other")
				r := valueRecord()
				if err := s.Seal(r); err != nil {
					t.Fatal(err)
				}
				line, _ := json.Marshal(r)
				writeFile(t, path, append(line, '\n'))
			case "reordered":
				lines := bytes.Split(bytes.TrimSpace(readFile(t, path)), []byte("\n"))
				writeFile(t, path, append(append(append([]byte(nil), lines[1]...), '\n'), append(lines[0], '\n')...))
			}
			before := readFile(t, path)
			b := fixtureAdapter(dir)
			if err := b.openEvidence(testProducer); err == nil {
				t.Fatal("unsafe startup accepted")
			}
			if !bytes.Equal(before, readFile(t, path)) {
				t.Fatal("startup rewrote damaged input")
			}
		})
	}
}

type failingStore struct {
	chain.MemoryStore
	fail bool
}

func (s *failingStore) Save(state chain.State) error {
	if s.fail {
		return errors.New("state disk full")
	}
	return s.MemoryStore.Save(state)
}

func TestStateFailureAfterAppendStopsRecordingAndTransport(t *testing.T) {
	dir := t.TempDir()
	a := fixtureAdapter(dir)
	if err := a.openEvidence(testProducer); err != nil {
		t.Fatal(err)
	}
	defer a.journal.Close()
	store := &failingStore{fail: true}
	a.sealer, _ = chain.New(store, testProducer, "source")
	stopped := false
	a.stop = func() { stopped = true }
	if err := a.emit(valueRecord(), "values"); err == nil {
		t.Fatal("state write error ignored")
	}
	before := readFile(t, a.cfg.Journal.Path)
	v, err := evidence.VerifyJournal(bytes.NewReader(before))
	if err != nil || !v.OK() {
		t.Fatal("journal was not durable before state save", v, err)
	}
	store.fail = false
	if err := a.emit(valueRecord(), "values"); err == nil {
		t.Fatal("uncertain commit allowed another record")
	}
	if !bytes.Equal(before, readFile(t, a.cfg.Journal.Path)) || !stopped || a.persistenceErr == nil || len(a.pub.(*capturePublisher).records) != 0 {
		t.Fatal("persistence failure did not stop both recording and transport")
	}
}

func TestTransportFailureLeavesJournalAndSourceLifecycleIntact(t *testing.T) {
	a := fixtureAdapter(t.TempDir())
	p := a.pub.(*capturePublisher)
	p.err = errors.New("queue full")
	if err := a.openEvidence(testProducer); err != nil {
		t.Fatal(err)
	}
	defer a.journal.Close()
	for i := 0; i < 3; i++ {
		if err := a.emit(valueRecord(), "values"); err != nil {
			t.Fatal("transport backpressure escaped to source", err)
		}
	}
	v, err := evidence.VerifyJournal(bytes.NewReader(readFile(t, a.cfg.Journal.Path)))
	if err != nil || !v.OK() || v.Records != 3 || a.persistenceErr != nil {
		t.Fatal("transport outage damaged local recording", v, err)
	}
}
