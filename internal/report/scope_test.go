package report

import (
	"bytes"
	"encoding/json"
	stdhtml "html"
	"strings"
	"testing"

	"github.com/RainerGewalt/trailsource/internal/evidence"
)

func renderHTML(t *testing.T, r *Report) string {
	t.Helper()
	var buf bytes.Buffer
	if err := r.WriteHTML(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func renderText(t *testing.T, r *Report) string {
	t.Helper()
	var buf bytes.Buffer
	if err := r.WriteText(&buf); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// The report says what verification covers and what it does not, in
// every state; it never claims the journal is unchanged since sealing,
// which a re-sealed or truncated journal would contradict.
func TestReportStatesWhatVerificationCovers(t *testing.T) {
	raw := loadFixture(t)
	tampered := bytes.Replace(raw, []byte(`"value":191`), []byte(`"value":199`), 1)
	for name, journal := range map[string][]byte{"ok": raw, "fail": tampered, "empty": nil} {
		r, err := Build(name+".jsonl", journal)
		if err != nil {
			t.Fatal(err)
		}
		for format, out := range map[string]string{"html": renderHTML(t, r), "text": renderText(t, r)} {
			out = strings.Join(strings.Fields(out), " ") // the text form is wrapped
			if strings.Contains(out, "unchanged since sealing") {
				t.Errorf("%s %s: claims the journal is unchanged since sealing", name, format)
			}
			for _, want := range []string{"re-seal", "cut off at the end", "context", "not covered by", "true"} {
				if !strings.Contains(out, want) {
					t.Errorf("%s %s: the scope statement lacks %q", name, format, want)
				}
			}
		}
	}
}

// An empty journal is not a verified one: no green OK, its own state.
func TestEmptyJournalIsNotOK(t *testing.T) {
	for _, journal := range [][]byte{nil, []byte("\n\n  \n")} {
		r, err := Build("empty.jsonl", journal)
		if err != nil {
			t.Fatal(err)
		}
		if r.OK || !strings.HasPrefix(r.VerifySummary(), "EMPTY") {
			t.Fatalf("empty journal: ok=%v summary %q", r.OK, r.VerifySummary())
		}
		html := renderHTML(t, r)
		if strings.Contains(html, `class="ok"`) || !strings.Contains(html, "no records") {
			t.Fatalf("empty journal shown as verified or without its own text:\n%s", html)
		}
	}
}

// The verdict claims only what verification checked. FAIL names what was
// observed and both known causes without deciding between them. OK does not
// say that a stream's first record links to one before it: a journal may
// begin with a restart whose earlier segment is not in the file, and that
// link cannot be checked.
func TestVerdictClaimsOnlyWhatWasChecked(t *testing.T) {
	raw := loadFixture(t)
	lines := bytes.SplitAfter(bytes.TrimRight(raw, "\n"), []byte("\n"))
	var midStream []byte // from the restart at seq 12: the earlier segment is missing
	for i, l := range lines {
		if i > 0 && bytes.Contains(l, []byte(`"chain_restart":true`)) {
			midStream = bytes.Join(lines[i:], nil)
			break
		}
	}
	if midStream == nil {
		t.Fatal("fixture has no restart after its first record")
	}
	tampered := bytes.Replace(raw, []byte(`"value":191`), []byte(`"value":199`), 1)

	for name, journal := range map[string][]byte{"ok": raw, "mid-stream": midStream} {
		r, err := Build(name+".jsonl", journal)
		if err != nil {
			t.Fatal(err)
		}
		if !r.OK {
			t.Fatalf("%s: want OK, got %s", name, r.VerifySummary())
		}
		v := r.Verdict()
		for _, claim := range []string{"links to the one before it", "starting condition"} {
			if strings.Contains(v, claim) {
				t.Errorf("%s: OK verdict claims %q: %q", name, claim, v)
			}
		}
		if !strings.Contains(v, "from each stream's first record in this file onwards") {
			t.Errorf("%s: OK verdict does not bound the chain to this file: %q", name, v)
		}
	}

	r, err := Build("tampered.jsonl", tampered)
	if err != nil {
		t.Fatal(err)
	}
	v := r.Verdict()
	for _, want := range []string{"Verification failed.", "may indicate", "chain_break", "cannot distinguish these causes"} {
		if !strings.Contains(v, want) {
			t.Errorf("FAIL verdict lacks %q: %q", want, v)
		}
	}
	for _, claim := range []string{"not what was sealed", "Usually", "FAIL means"} {
		if strings.Contains(v, claim) {
			t.Errorf("FAIL verdict decides the cause (%q): %q", claim, v)
		}
	}
	if text := strings.Join(strings.Fields(renderText(t, r)), " "); !strings.Contains(text, v) {
		t.Errorf("text report does not carry the FAIL verdict")
	}
}

// The scope statement at the end of every report claims only what
// verification checked, like the verdict. Chain links are checked from each
// stream's first record in the file onwards: a journal cut at its start to
// an ordinary record fails, but one that begins with a restart whose earlier
// segment is missing verifies with a notice, and one cut off at the end of
// a stream verifies as well. The statement must say so in text and HTML, in
// every state.
func TestScopeClaimsOnlyWhatWasChecked(t *testing.T) {
	raw := loadFixture(t)
	lines := bytes.SplitAfter(bytes.TrimRight(raw, "\n"), []byte("\n"))
	var midStream []byte // from the restart at seq 12: the earlier segment is missing
	for i, l := range lines {
		if i > 0 && bytes.Contains(l, []byte(`"chain_restart":true`)) {
			midStream = bytes.Join(lines[i:], nil)
			break
		}
	}
	if midStream == nil {
		t.Fatal("fixture has no restart after its first record")
	}
	headCut := bytes.Join(lines[1:], nil)            // starts at seq 1, an ordinary record
	tailCut := bytes.Join(lines[:len(lines)-2], nil) // the last two records are gone
	tampered := bytes.Replace(raw, []byte(`"value":191`), []byte(`"value":199`), 1)

	// The behaviour the statement describes.
	for name, c := range map[string]struct {
		journal []byte
		ok      bool
		detail  string
	}{
		"head cut to an ordinary record": {headCut, false, "is not genesis and not a restart"},
		"mid-stream from a restart":      {midStream, true, "previous segment not in this journal"},
		"tail cut":                       {tailCut, true, ""},
	} {
		r, err := Build("journal.jsonl", c.journal)
		if err != nil {
			t.Fatal(err)
		}
		if r.OK != c.ok {
			t.Fatalf("%s: ok=%v, want %v (%s)", name, r.OK, c.ok, r.VerifySummary())
		}
		found := c.detail == ""
		for _, is := range r.Verify.Issues {
			found = found || strings.Contains(is.Detail, c.detail)
		}
		if !found {
			t.Fatalf("%s: no finding or notice with %q: %+v", name, c.detail, r.Verify.Issues)
		}
	}

	// The statement itself.
	for name, journal := range map[string][]byte{"ok": raw, "mid-stream": midStream, "tail cut": tailCut, "fail": tampered, "empty": nil} {
		r, err := Build(name+".jsonl", journal)
		if err != nil {
			t.Fatal(err)
		}
		for format, out := range map[string]string{"html": stdhtml.UnescapeString(renderHTML(t, r)), "text": renderText(t, r)} {
			out = strings.Join(strings.Fields(out), " ") // the text form is wrapped
			for _, claim := range []string{"each record links to the one before it, so", "cut off at the end of the journal"} {
				if strings.Contains(out, claim) {
					t.Errorf("%s %s: the scope statement claims more than was checked: %q", name, format, claim)
				}
			}
			for _, want := range []string{
				"from each stream's first record in this file onwards",
				"records before it cannot be checked",
				"cut off at the end of a stream",
				"a stream removed entirely",
				"anchor outside the file",
				"re-seal all of it",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("%s %s: the scope statement lacks %q", name, format, want)
				}
			}
		}
	}
}

// With FAIL the findings come right after the verdict, before the streams.
func TestFailShowsFindingsFirst(t *testing.T) {
	raw := bytes.Replace(loadFixture(t), []byte(`"value":191`), []byte(`"value":199`), 1)
	r, err := Build("tampered.jsonl", raw)
	if err != nil {
		t.Fatal(err)
	}
	html := renderHTML(t, r)
	f, s := strings.Index(html, "<h2>Findings"), strings.Index(html, "<h2>Stream")
	if f < 0 || s < 0 || f > s {
		t.Fatalf("findings at %d, first stream at %d: findings must come first on FAIL", f, s)
	}
	text := renderText(t, r)
	if f, s := strings.Index(text, "Findings ("), strings.Index(text, "\nStream "); f < 0 || f > s {
		t.Fatalf("text: findings at %d, first stream at %d", f, s)
	}
}

// The endpoint shown is the one in the hashed payload of the stream's
// records, not the unhashed context, which anyone can edit without
// verification noticing.
func TestEndpointComesFromHashedRecords(t *testing.T) {
	var out bytes.Buffer
	for _, line := range bytes.Split(bytes.TrimSpace(loadFixture(t)), []byte("\n")) {
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatal(err)
		}
		m["context"].(map[string]any)["endpoint"] = "opc.tcp://other-plant.example:4840/line9"
		b, _ := json.Marshal(m)
		out.Write(append(b, '\n'))
	}
	r, err := Build("ctx-edited.jsonl", out.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !r.OK {
		t.Fatalf("an edited context must still verify (it is not hashed): %s", r.VerifySummary())
	}
	if got := r.Streams[0].Endpoint; got != "opc.tcp://127.0.0.1:48400/trailsource" {
		t.Fatalf("endpoint %q, want the hashed one", got)
	}
	if html := renderHTML(t, r); strings.Contains(html, "other-plant") {
		t.Fatal("the report shows the unhashed context endpoint")
	}
}

// A report that is passed on carries the journal's file name, not the
// directory it was read from, and says which TrailSource made it, when,
// and which producer versions wrote the stream.
func TestReportNamesFileGeneratorAndProducerVersion(t *testing.T) {
	r, err := Build("/home/someone/plant/journal/plant-a.jsonl", loadFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	r.Generator, r.GeneratedAt = "9.9.9-test", "2026-11-11T12:00:00.000Z"
	for format, out := range map[string]string{"html": renderHTML(t, r), "text": renderText(t, r)} {
		if strings.Contains(out, "/home/someone") {
			t.Errorf("%s: the local path reaches the report", format)
		}
		for _, want := range []string{"plant-a.jsonl", "9.9.9-test", "2026-11-11T12:00:00.000Z", "0.2.0-test"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s lacks %q", format, want)
			}
		}
	}
}

// The first record of a new stream is its start, not a restart after a
// segment that is missing from the journal.
func TestGenesisRestartIsTheStartOfTheStream(t *testing.T) {
	r, err := Build("fixture.jsonl", loadFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	first := r.Verify.Issues[0]
	if first.Kind != evidence.IssueRestart || strings.Contains(first.Detail, "not in this journal") || !strings.Contains(first.Detail, "start of the stream") {
		t.Fatalf("genesis restart detail: %q", first.Detail)
	}
}

// Below 560 px the gap, restart and finding tables become stacked entries
// whose cells carry their column name, so nothing needs sideways scrolling.
func TestNarrowTablesStack(t *testing.T) {
	raw := bytes.Replace(loadFixture(t), []byte(`"value":191`), []byte(`"value":199`), 1)
	r, err := Build("tampered.jsonl", raw)
	if err != nil {
		t.Fatal(err)
	}
	html := renderHTML(t, r)
	for _, want := range []string{`<table class="stack">`, `data-label="Duration"`, `data-label="Reason"`, `data-label="Class"`, `data-label="Detail"`, `data-label="Anchor"`,
		"@media (max-width:560px)", "table.stack tr{display:block"} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML lacks %q", want)
		}
	}
}
