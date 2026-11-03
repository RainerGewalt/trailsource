// Package chain seals records into one hash chain per stream and persists the
// chain position (seq, last chain_hash) so sequence numbers survive restarts.
package chain

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/RainerGewalt/trailsource/internal/evidence"
)

// State is the persisted chain position of one stream.
type State struct {
	StreamID  string `json:"stream_id"`
	Seq       uint64 `json:"seq"`        // seq of the last sealed record
	ChainHash string `json:"chain_hash"` // chain_hash of the last sealed record
	UpdatedAt string `json:"updated_at"`
	Sealed    bool   `json:"sealed"` // false until the first record exists
}

// Store persists State. The file implementation writes atomically (tmp+rename).
type Store interface {
	Load() (State, bool, error)
	Save(State) error
}

// FileStore keeps the state in one JSON file.
type FileStore struct{ Path string }

// Load reads the state file. The boolean is false when no file exists yet.
func (f FileStore) Load() (State, bool, error) {
	raw, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, err
	}
	var s State
	if err := json.Unmarshal(raw, &s); err != nil {
		return State{}, false, fmt.Errorf("parse chain state %s: %w", f.Path, err)
	}
	return s, true, nil
}

// Save writes the state atomically.
func (f FileStore) Save(s State) error {
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, f.Path)
}

// MemoryStore is a Store for tests.
type MemoryStore struct {
	state State
	has   bool
}

func (m *MemoryStore) Load() (State, bool, error) { return m.state, m.has, nil }
func (m *MemoryStore) Save(s State) error         { m.state, m.has = s, true; return nil }

// Sealer assigns seq/prev_hash, seals records and persists the position.
type Sealer struct {
	mu       sync.Mutex
	store    Store
	producer evidence.Producer
	stream   string
	state    State
	restart  bool // true until the first record of this process is sealed
	now      func() time.Time
}

// New loads the stream position from store. The first record sealed by the
// returned Sealer carries chain_restart=true with the last known chain_hash
// (or genesis) as anchor_hash.
func New(store Store, producer evidence.Producer, streamID string) (*Sealer, error) {
	state, found, err := store.Load()
	if err != nil {
		return nil, err
	}
	if found && state.StreamID != streamID {
		return nil, fmt.Errorf("chain state belongs to stream %q, not %q", state.StreamID, streamID)
	}
	if !found {
		state = State{StreamID: streamID, ChainHash: evidence.GenesisHash}
	}
	return &Sealer{store: store, producer: producer, stream: streamID, state: state, restart: true, now: time.Now}, nil
}

// Position returns the seq and chain_hash of the last sealed record.
func (s *Sealer) Position() (uint64, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Seq, s.state.ChainHash, s.state.Sealed
}

// Seal fills producer, stream_id, seq, prev_hash, chain_restart/anchor_hash and
// time.recorded_at, seals r and persists the new position. The record is only
// considered sealed when the state was persisted; on a persistence error the
// in-memory position is left unchanged.
func (s *Sealer) Seal(r *evidence.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	r.Schema = evidence.Schema
	r.Producer = s.producer
	r.StreamID = s.stream
	r.Time.RecordedAt = evidence.FormatTime(s.now())
	if r.Time.Source == "" {
		r.Time.Source = evidence.TimeSourceSystem
	}

	anchor := s.state.ChainHash
	if !s.state.Sealed {
		anchor = evidence.GenesisHash
	}
	if s.restart {
		r.ChainRestart = true
		r.AnchorHash = anchor
	} else {
		r.ChainRestart = false
		r.AnchorHash = ""
	}
	r.PrevHash = anchor
	if s.state.Sealed {
		r.Seq = evidence.Seq(s.state.Seq + 1)
	} else {
		r.Seq = 0
	}

	if err := evidence.Seal(r); err != nil {
		return err
	}
	next := State{
		StreamID:  s.stream,
		Seq:       uint64(r.Seq),
		ChainHash: r.ChainHash,
		UpdatedAt: r.Time.RecordedAt,
		Sealed:    true,
	}
	if err := s.store.Save(next); err != nil {
		return fmt.Errorf("persist chain state: %w", err)
	}
	s.state = next
	s.restart = false
	return nil
}
