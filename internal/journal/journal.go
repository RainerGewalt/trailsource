// Package journal appends sealed records to a JSON Lines file.
package journal

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/RainerGewalt/trailsource/internal/evidence"
)

// Writer appends one JSON object per line. It is safe for concurrent use.
type Writer struct {
	mu     sync.Mutex
	f      syncFile
	path   string
	failed error
}

type syncFile interface {
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

// Open opens (or creates) the journal file in append mode.
func Open(path string) (*Writer, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return &Writer{f: f, path: path}, nil
}

// Path returns the journal file path.
func (w *Writer) Path() string { return w.path }

// Append writes v as one line. The bytes written are returned so callers can
// publish exactly what was journaled.
func (w *Writer) Append(v any) ([]byte, error) {
	line, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.failed != nil {
		return nil, fmt.Errorf("journal stopped after write/sync failure: %w", w.failed)
	}
	n, err := w.f.Write(append(line, '\n'))
	if err == nil && n != len(line)+1 {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = w.f.Sync()
	}
	if err != nil {
		w.failed = err
		return nil, err
	}
	return line, nil
}

// Close syncs and closes the file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	err := w.f.Sync()
	closeErr := w.f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

// LastRecord checks the whole existing journal before returning its tail.
// Partial lines, invalid chains, foreign streams and reordered records stop
// startup without modifying a byte. An empty file is also the manual rotation
// case; it supplies no tail and does not reset the saved state.
func (w *Writer) LastRecord(stream string) (*evidence.Record, error) {
	f, err := os.Open(w.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || stat.Size() == 0 {
		return nil, err
	}
	var end [1]byte
	if _, err := f.ReadAt(end[:], stat.Size()-1); err != nil {
		return nil, err
	}
	if end[0] != '\n' {
		return nil, fmt.Errorf("journal has an unterminated last line; preserve it for investigation")
	}
	report, err := evidence.VerifyJournal(f)
	if err != nil {
		return nil, err
	}
	if !report.OK() {
		return nil, fmt.Errorf("existing journal does not verify; preserve it for investigation")
	}
	if len(report.Streams) != 1 || report.Streams[0].StreamID != stream {
		return nil, fmt.Errorf("journal must contain only stream %q", stream)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(f)
	var last *evidence.Record
	for {
		var r evidence.Record
		if err := dec.Decode(&r); err == io.EOF {
			return last, nil
		} else if err != nil {
			return nil, err
		}
		if last != nil && r.Seq <= last.Seq {
			return nil, fmt.Errorf("journal records are not in increasing sequence order")
		}
		last = &r
	}
}
