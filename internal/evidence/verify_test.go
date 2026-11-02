package evidence

import (
	"encoding/json"
	"strings"
	"testing"
)

func sealedChain(t *testing.T, n int) []Record {
	t.Helper()
	out := make([]Record, 0, n)
	prev := GenesisHash
	for i := 0; i < n; i++ {
		r := baseRecord()
		r.Seq = uint64(i)
		r.PrevHash = prev
		r.Payload = map[string]any{"i": i}
		if err := Seal(&r); err != nil {
			t.Fatal(err)
		}
		prev = r.ChainHash
		out = append(out, r)
	}
	return out
}

func journal(recs []Record) string {
	var sb strings.Builder
	for _, r := range recs {
		b, _ := json.Marshal(r)
		sb.Write(b)
		sb.WriteByte('\n')
	}
	return sb.String()
}

func TestVerifyIntactChain(t *testing.T) {
	recs := sealedChain(t, 5)
	rep, err := VerifyJournal(strings.NewReader(journal(recs)))
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK() || rep.Records != 5 || len(rep.Streams) != 1 {
		t.Fatalf("unexpected report %+v", rep)
	}
}

func TestVerifyDetectsTamperedPayload(t *testing.T) {
	recs := sealedChain(t, 3)
	recs[1].Payload = map[string]any{"i": 99}
	rep, _ := VerifyJournal(strings.NewReader(journal(recs)))
	if rep.OK() {
		t.Fatal("tampered payload accepted")
	}
	kinds := map[IssueKind]bool{}
	for _, i := range rep.Issues {
		kinds[i.Kind] = true
	}
	if !kinds[IssueContentHash] {
		t.Fatalf("expected content_hash issue, got %+v", rep.Issues)
	}
	if kinds[IssuePrevHash] {
		t.Fatalf("payload tamper must not be reported as a link break: %+v", rep.Issues)
	}
}

func TestVerifyDetectsRemovedRecordAsBreak(t *testing.T) {
	recs := sealedChain(t, 4)
	recs = append(recs[:2], recs[3:]...)
	rep, _ := VerifyJournal(strings.NewReader(journal(recs)))
	if rep.OK() {
		t.Fatal("missing record accepted")
	}
	var sawPrev, sawGap bool
	for _, i := range rep.Issues {
		sawPrev = sawPrev || i.Kind == IssuePrevHash
		sawGap = sawGap || i.Kind == IssueGap
	}
	if !sawPrev || !sawGap {
		t.Fatalf("expected prev_hash and gap issues, got %+v", rep.Issues)
	}
}

func TestVerifyReportsRestartSeparately(t *testing.T) {
	recs := sealedChain(t, 2)
	r := baseRecord()
	r.Seq = 7
	r.ChainRestart = true
	r.AnchorHash = recs[1].ChainHash
	r.PrevHash = r.AnchorHash
	if err := Seal(&r); err != nil {
		t.Fatal(err)
	}
	recs = append(recs, r)
	rep, _ := VerifyJournal(strings.NewReader(journal(recs)))
	if !rep.OK() {
		t.Fatalf("restart must not fail verification: %+v", rep.Issues)
	}
	if rep.Streams[0].Restarts != 1 || rep.Streams[0].Gaps != 0 {
		t.Fatalf("restart not counted: %+v", rep.Streams[0])
	}
}
