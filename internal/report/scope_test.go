package report

import (
	"bytes"
	"encoding/json"
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
