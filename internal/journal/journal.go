// Package journal appends sealed records to a JSON Lines file.
package journal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Writer appends one JSON object per line. It is safe for concurrent use.
type Writer struct {
	mu   sync.Mutex
	f    *os.File
	path string
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
	if _, err := w.f.Write(append(line, '\n')); err != nil {
		return nil, err
	}
	return line, nil
}

// Close syncs and closes the file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.f.Sync()
	return w.f.Close()
}
