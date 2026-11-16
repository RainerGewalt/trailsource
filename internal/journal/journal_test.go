package journal

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type faultFile struct {
	bytes.Buffer
	writeErr, syncErr, closeErr error
	short                       bool
	syncs, closes               int
}

func (f *faultFile) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	if f.short {
		return f.Buffer.Write(p[:len(p)-1])
	}
	return f.Buffer.Write(p)
}
func (f *faultFile) Sync() error  { f.syncs++; return f.syncErr }
func (f *faultFile) Close() error { f.closes++; return f.closeErr }

func TestAppendFailureCannotBeFollowedByAnotherLine(t *testing.T) {
	for _, kind := range []string{"write", "short", "sync"} {
		t.Run(kind, func(t *testing.T) {
			f := &faultFile{}
			problem := errors.New("injected disk failure")
			switch kind {
			case "write":
				f.writeErr = problem
			case "short":
				f.short = true
			case "sync":
				f.syncErr = problem
			}
			w := &Writer{f: f}
			if _, err := w.Append(map[string]any{"n": 1}); err == nil {
				t.Fatal("write/short write/sync failure accepted")
			}
			before := f.String()
			f.writeErr, f.syncErr, f.short = nil, nil, false
			if _, err := w.Append(map[string]any{"n": 2}); err == nil {
				t.Fatal("uncertain journal continued without restart")
			}
			if f.String() != before {
				t.Fatal("additional bytes written after uncertain commit")
			}
		})
	}
}

func TestAppendSyncsEachRecordAndReturnsExactBytes(t *testing.T) {
	f := &faultFile{}
	w := &Writer{f: f}
	for i := 1; i <= 3; i++ {
		line, err := w.Append(map[string]any{"n": i})
		if err != nil {
			t.Fatal(err)
		}
		if f.syncs != i || !bytes.HasSuffix(f.Bytes(), append(line, '\n')) {
			t.Fatalf("record %d not durably appended: %q, syncs=%d", i, f.Bytes(), f.syncs)
		}
	}
	if _, err := w.Append(make(chan int)); err == nil {
		t.Fatal("unserializable value accepted")
	}
	if f.syncs != 3 {
		t.Fatal("marshal error wrote to the file")
	}
	if _, err := w.Append(map[string]any{"n": 4}); err != nil {
		t.Fatal("pre-write validation failure should allow retry", err)
	}
}

func TestCloseReportsSyncFailureAndStillCloses(t *testing.T) {
	problem := errors.New("sync failed")
	f := &faultFile{syncErr: problem}
	if err := (&Writer{f: f}).Close(); !errors.Is(err, problem) || f.closes != 1 {
		t.Fatalf("error=%v, closes=%d", err, f.closes)
	}
}

func TestLastRecordRejectsTruncatedOrMalformedInputWithoutRewriting(t *testing.T) {
	for _, raw := range []string{"{", "{}\n", "\n", "{\"stream_id\":\"s\"}\n", "null\n"} {
		t.Run(raw, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "journal.jsonl")
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			w, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()
			if _, err := w.LastRecord("s"); err == nil {
				t.Fatal("invalid existing journal accepted")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != raw {
				t.Fatal("invalid journal was changed")
			}
		})
	}
}
