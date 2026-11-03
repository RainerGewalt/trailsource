package evidence

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func sealedChain(t *testing.T, n int) []Record {
	t.Helper()
	out := make([]Record, 0, n)
	prev := GenesisHash
	for i := 0; i < n; i++ {
		r := baseRecord()
		r.Seq = Seq(i)
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
	if !kinds[IssueHashMismatch] {
		t.Fatalf("expected hash_mismatch, got %+v", rep.Issues)
	}
	if kinds[IssueChainBreak] {
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
		sawPrev = sawPrev || i.Kind == IssueChainBreak
		sawGap = sawGap || i.Kind == IssueSeqGap
	}
	if !sawPrev || !sawGap {
		t.Fatalf("expected chain_break and seq_gap, got %+v", rep.Issues)
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

func TestVerifyRestartWithWrongAnchorIsBreak(t *testing.T) {
	recs := sealedChain(t, 2)
	r := baseRecord()
	r.Seq = 7
	r.ChainRestart = true
	r.AnchorHash = GenesisHash // previous segment is present, so genesis is wrong
	r.PrevHash = GenesisHash
	if err := Seal(&r); err != nil {
		t.Fatal(err)
	}
	rep, _ := VerifyJournal(strings.NewReader(journal(append(recs, r))))
	if rep.OK() || rep.Streams[0].Breaks != 1 || rep.Streams[0].Restarts != 0 {
		t.Fatalf("mismatching anchor must be a break: %+v %+v", rep.Streams[0], rep.Issues)
	}
}

func issuesOnLine(rep Report, line int) []IssueKind {
	var out []IssueKind
	for _, i := range rep.Issues {
		if i.Line == line {
			out = append(out, i.Kind)
		}
	}
	return out
}

// An edited payload without new hashes fails both hashes of its own line and
// nothing else: the next record links to the declared chain_hash.
func TestVerifyEditedPayloadIsLocalHashMismatch(t *testing.T) {
	recs := sealedChain(t, 4)
	recs[1].Payload = map[string]any{"i": 42}
	rep, _ := VerifyJournal(strings.NewReader(journal(recs)))
	got := issuesOnLine(rep, 2)
	if len(got) != 2 || got[0] != IssueHashMismatch || got[1] != IssueHashMismatch {
		t.Fatalf("line 2: want content and chain hash_mismatch, got %v (%+v)", got, rep.Issues)
	}
	if n := len(issuesOnLine(rep, 3)); n != 0 {
		t.Fatalf("line 3 must stay clean, got %+v", rep.Issues)
	}
}

// An edited payload that is re-sealed passes on its own line; the following
// record then no longer links to it.
func TestVerifyResealedEditBreaksNextLine(t *testing.T) {
	recs := sealedChain(t, 4)
	recs[1].Payload = map[string]any{"i": 42}
	if err := Seal(&recs[1]); err != nil {
		t.Fatal(err)
	}
	rep, _ := VerifyJournal(strings.NewReader(journal(recs)))
	if got := issuesOnLine(rep, 2); len(got) != 0 {
		t.Fatalf("re-sealed line 2 should verify on its own, got %v", got)
	}
	got := issuesOnLine(rep, 3)
	if len(got) != 1 || got[0] != IssueChainBreak {
		t.Fatalf("line 3: want chain_break, got %v (%+v)", got, rep.Issues)
	}
	if rep.OK() {
		t.Fatal("re-sealed edit accepted")
	}
}

func TestSeqIsWrittenAsCanonicalDecimalString(t *testing.T) {
	r := baseRecord()
	r.Seq = 18446744073709551615
	if err := Seal(&r); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(r)
	if !strings.Contains(string(raw), `"seq":"18446744073709551615"`) {
		t.Fatalf("seq not a decimal string: %s", raw)
	}
	var back Record
	if err := json.Unmarshal(raw, &back); err != nil || back.Seq != r.Seq {
		t.Fatalf("round trip failed: %v %d", err, back.Seq)
	}
	for _, bad := range []string{`"01"`, `"-1"`, `"1e3"`, `""`, `"18446744073709551616"`, `"1.0"`} {
		var s Seq
		if err := json.Unmarshal([]byte(bad), &s); err == nil {
			t.Errorf("%s accepted as seq", bad)
		}
	}
	var legacy Seq
	if err := json.Unmarshal([]byte(`42`), &legacy); err != nil || legacy != 42 {
		t.Fatalf("integer seq of older journals must still decode: %v", err)
	}
}

// A journal written with integer seq (before the string rule) still
// verifies; the verifier says so once per stream as a notice.
func TestVerifyAcceptsIntegerSeqWithNotice(t *testing.T) {
	var sb strings.Builder
	prev := GenesisHash
	for i := 0; i < 3; i++ {
		r := baseRecord()
		r.Seq = Seq(i)
		r.PrevHash = prev
		r.Payload = map[string]any{"i": i}
		raw, _ := json.Marshal(r)
		var m map[string]any
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.UseNumber()
		_ = dec.Decode(&m)
		m["seq"] = json.Number(strconv.Itoa(i)) // the old wire form
		m["content_hash"], _ = Hash(m["payload"])
		delete(m, "chain_hash")
		m["chain_hash"], _ = Hash(coreFromMap(m))
		prev = m["chain_hash"].(string)
		line, _ := CanonicalJCS(m)
		sb.Write(line)
		sb.WriteByte('\n')
	}
	rep, err := VerifyJournal(strings.NewReader(sb.String()))
	if err != nil {
		t.Fatal(err)
	}
	notices := 0
	for _, i := range rep.Issues {
		if i.Kind == IssueLegacyIntegerSeq {
			notices++
		}
	}
	if !rep.OK() || notices != 1 || rep.Streams[0].LastSeq != 2 {
		t.Fatalf("integer journal: ok=%v notices=%d %+v", rep.OK(), notices, rep.Issues)
	}
}

func TestVerifyRejectsNonCanonicalSeq(t *testing.T) {
	recs := sealedChain(t, 2)
	raw := journal(recs)
	raw = strings.Replace(raw, `"seq":"1"`, `"seq":"01"`, 1)
	rep, _ := VerifyJournal(strings.NewReader(raw))
	found := false
	for _, i := range rep.Issues {
		found = found || i.Kind == IssueSchemaInvalid
	}
	if rep.OK() || !found {
		t.Fatalf("leading zero accepted: %+v", rep.Issues)
	}
}
