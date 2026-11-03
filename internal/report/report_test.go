package report

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RainerGewalt/trailsource/internal/evidence"
	"github.com/RainerGewalt/trailsource/internal/status"
)

var update = flag.Bool("update", false, "rewrite testdata/report fixtures and golden files")

const fixtureDir = "../../testdata/report"

// fixture builds a small, deterministic journal of one OPC UA stream:
// start, initial inventory, values, a lost connection that is restored after
// 15 s, an added node, a bad value, an orderly stop and a restart 10 s later.
func fixture(t *testing.T) []byte {
	t.Helper()
	t0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	const sid = "plant-a-ext-01-extruder-01-opcua"
	producer := evidence.Producer{Kind: evidence.ProducerSourceBridge, ID: "trailsource-opcua-extruder-01", Version: "0.2.0-test"}
	ctx := map[string]any{"endpoint": "opc.tcp://127.0.0.1:48400/trailsource", "protocol": "opcua"}
	cfgPolicy := evidence.Policy{ID: "config:opcua-local.yaml", Version: "1", Hash: evidence.Sum([]byte("config")), Kind: "config"}

	node := func(path, typ string) map[string]any {
		return map[string]any{"node_id": "ns=2;s=" + path, "browse_path": path, "browse_name": path[strings.LastIndex(path, ".")+1:], "data_type": typ}
	}
	gen1 := []map[string]any{
		node("Plant.Line01.Extruder01.MeltPressureBar", "float64"),
		node("Plant.Line01.Extruder01.ScrewSpeedRpm", "float64"),
		node("Plant.Line01.Extruder01.BarrelZone01.TempActualC", "float64"),
	}
	added := node("Plant.Line01.Extruder01.BarrelZone07.TempActualC", "float64")
	gen2 := append(append([]map[string]any{}, gen1...), added)
	fp1, fp2 := evidence.Sum([]byte("gen1")), evidence.Sum([]byte("gen2"))

	var out bytes.Buffer
	prev := evidence.GenesisHash
	var seq uint64
	emit := func(at time.Duration, restart bool, eventType string, policy evidence.Policy, payload map[string]any) {
		ts := evidence.FormatTime(t0.Add(at))
		r := evidence.Record{Schema: evidence.Schema, Producer: producer, StreamID: sid, Seq: evidence.Seq(seq), PrevHash: prev,
			EventType: eventType, Time: evidence.Time{RecordedAt: ts, ReadAt: ts, Source: evidence.TimeSourceSystem},
			Actor: evidence.Actor{Auth: evidence.ActorAuthSelfDeclared, ID: producer.ID, Roles: []string{"collector"}}, Policy: policy, Payload: payload, Context: ctx}
		if restart {
			r.ChainRestart, r.AnchorHash = true, prev
		}
		if err := evidence.Seal(&r); err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(append(b, '\n'))
		prev = r.ChainHash
		seq++
	}
	conn := func(state, code, reason string) map[string]any {
		p := map[string]any{"state": state, "reason_code": code, "endpoint": ctx["endpoint"]}
		if reason != "" {
			p["reason"] = reason
		}
		return p
	}
	start := func(code, chainState string) map[string]any {
		return map[string]any{"reason_code": code, "chain_state": chainState, "producer_version": producer.Version, "endpoint": ctx["endpoint"]}
	}
	value := func(path string, v any, quality string, sourceTS any, gen uint64, fp string) (evidence.Policy, map[string]any) {
		return evidence.Policy{ID: sid + "/addressspace", Version: itoa(gen), Hash: fp, Kind: "addressspace"},
			map[string]any{"native_node_id": "ns=2;s=" + path, "entity_key": sid + "/" + strings.ToLower(strings.ReplaceAll(path, ".", "-")),
				"value": v, "quality": quality, "source_timestamp": sourceTS, "inventory_hash": fp, "inventory_generation": gen}
	}
	st := func(at time.Duration) string { return evidence.FormatTimeMicro(t0.Add(at)) }

	emit(0, true, status.EventBridgeRestarted, cfgPolicy, start(status.Restart, "new"))
	emit(0, false, "source.connection.changed", cfgPolicy, conn("connected", status.ConnectionEstablished, ""))
	emit(200*time.Millisecond, false, "source.inventory.changed", cfgPolicy, map[string]any{
		"reason_code": status.InventoryInitial, "change_kind": "initial", "generation": 1, "fingerprint": fp1, "previous_fingerprint": nil,
		"node_count": len(gen1), "truncated": false, "nodes": gen1, "diff": map[string]any{"added": nil, "removed": nil, "changed": nil}})
	p, v := value("Plant.Line01.Extruder01.MeltPressureBar", 190.5, "good", st(time.Second), 1, fp1)
	emit(time.Second, false, "source.value.observed", p, v)
	p, v = value("Plant.Line01.Extruder01.ScrewSpeedRpm", 85.1, "good", st(time.Second), 1, fp1)
	emit(time.Second, false, "source.value.observed", p, v)
	p, v = value("Plant.Line01.Extruder01.BarrelZone01.TempActualC", 170.2, "good", st(9500*time.Millisecond), 1, fp1)
	emit(9500*time.Millisecond, false, "source.value.observed", p, v)
	emit(10*time.Second, false, "source.connection.changed", cfgPolicy, conn("disconnected", status.ConnectionLost, "opc connection state Disconnected"))
	restored := conn("connected", status.ConnectionRestored, "")
	restored["down_ms"] = 15000
	emit(25*time.Second, false, "source.connection.changed", cfgPolicy, restored)
	p, v = value("Plant.Line01.Extruder01.MeltPressureBar", 191, "good", st(26*time.Second), 1, fp1)
	emit(26*time.Second, false, "source.value.observed", p, v)
	emit(30*time.Second, false, "source.inventory.changed", cfgPolicy, map[string]any{
		"reason_code": status.NodeAdded, "change_kind": "changed", "generation": 2, "fingerprint": fp2, "previous_fingerprint": fp1,
		"node_count": len(gen2), "truncated": false, "nodes": gen2, "diff": map[string]any{"added": []any{added}, "removed": nil, "changed": nil}})
	p, v = value("Plant.Line01.Extruder01.BarrelZone07.TempActualC", nil, "bad", nil, 2, fp2)
	v["reason_code"] = status.QualityBad
	emit(31*time.Second, false, "source.value.observed", p, v)
	emit(40*time.Second, false, "source.connection.changed", cfgPolicy, conn("disconnected", status.ConnectionClosed, "adapter stopping"))
	emit(50*time.Second, true, status.EventBridgeRestarted, cfgPolicy, start(status.Restart, "resumed"))
	emit(50*time.Second, false, "source.connection.changed", cfgPolicy, conn("connected", status.ConnectionEstablished, ""))
	p, v = value("Plant.Line01.Extruder01.MeltPressureBar", 189.9, "good", st(51*time.Second), 2, fp2)
	emit(51*time.Second, false, "source.value.observed", p, v)
	return out.Bytes()
}

func itoa(n uint64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func loadFixture(t *testing.T) []byte {
	t.Helper()
	path := filepath.Join(fixtureDir, "journal.jsonl")
	if *update {
		if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, fixture(t), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/report -update)", err)
	}
	return raw
}

func TestReportSummarisesFixture(t *testing.T) {
	r, err := Build("testdata/report/journal.jsonl", loadFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if !r.OK || len(r.Streams) != 1 {
		t.Fatalf("fixture must verify with one stream: %s %+v", r.VerifySummary(), r.Verify.Issues)
	}
	s := r.Streams[0]
	if s.Records != 15 || s.First != "2026-11-02T10:00:00.000Z" || s.Last != "2026-11-02T10:00:51.000Z" || s.Span != "51s" {
		t.Fatalf("period/records wrong: %+v", s)
	}
	counts := map[string]int{}
	for _, e := range s.Events {
		counts[e.EventType] = e.Count
	}
	if counts["source.value.observed"] != 6 || counts["source.connection.changed"] != 5 || counts["source.inventory.changed"] != 2 || counts[status.EventBridgeRestarted] != 2 {
		t.Fatalf("event counts wrong: %+v", s.Events)
	}
	if s.Values.Bad != 1 || s.Values.NoSourceTimestamp != 1 || s.Values.Nodes != 4 {
		t.Fatalf("value stats wrong: %+v", s.Values)
	}
	if len(s.Generations) != 2 || s.Generations[1].ReasonCode != status.NodeAdded ||
		len(s.Generations[1].Added) != 1 || s.Generations[1].Added[0] != "Plant.Line01.Extruder01.BarrelZone07.TempActualC (float64)" {
		t.Fatalf("generations wrong: %+v", s.Generations)
	}
	if len(s.Gaps) != 2 {
		t.Fatalf("want 2 gaps, got %+v", s.Gaps)
	}
	g := s.Gaps[0]
	if g.Kind != "connection" || g.Duration != "15s" || g.ReasonCode != status.ConnectionLost ||
		g.Reason != "opc connection state Disconnected" || g.LastDataBefore != "2026-11-02T10:00:09.500Z" {
		t.Fatalf("connection gap wrong: %+v", g)
	}
	if g := s.Gaps[1]; g.Kind != "adapter" || g.Duration != "10s" || g.ReasonCode != status.ConnectionClosed {
		t.Fatalf("adapter gap wrong: %+v", g)
	}
	if s.LastEvent != status.ConnectionEstablished || s.LastSeen != "2026-11-02T10:00:51.000Z" {
		t.Fatalf("end state wrong: %s %s", s.LastEvent, s.LastSeen)
	}
	if len(s.Restarts) != 2 || !s.Restarts[0].AnchorGenesis || s.Restarts[1].AnchorGenesis || s.Restarts[1].AfterPrevious != "10s" {
		t.Fatalf("restarts wrong: %+v", s.Restarts)
	}
}

func TestReportTextGolden(t *testing.T) {
	r, err := Build("testdata/report/journal.jsonl", loadFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := r.WriteText(&buf); err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join(fixtureDir, "journal.txt")
	if *update {
		if err := os.WriteFile(golden, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if buf.String() != string(want) {
		t.Fatalf("text report differs from %s (run with -update after checking):\n%s", golden, buf.String())
	}
}

func TestReportHTMLIsSelfContained(t *testing.T) {
	r, err := Build("testdata/report/journal.jsonl", loadFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := r.WriteHTML(&buf); err != nil {
		t.Fatal(err)
	}
	html := buf.String()
	for _, want := range []string{"<!doctype html>", "prefers-color-scheme:dark", "width=device-width",
		"source.node_added", "BarrelZone07", "15s", "opc connection state Disconnected", "OK: 15 records"} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML lacks %q", want)
		}
	}
	for _, banned := range []string{"<script", "<link", "src=", "@import", "url("} {
		if strings.Contains(html, banned) {
			t.Errorf("HTML must not reference external resources, found %q", banned)
		}
	}
}

// A changed value in the journal fails verification; the report still
// renders and names the line.
func TestReportShowsTamperedJournal(t *testing.T) {
	raw := loadFixture(t)
	tampered := bytes.Replace(raw, []byte(`"value":191`), []byte(`"value":199`), 1)
	if bytes.Equal(raw, tampered) {
		t.Fatal("fixture lacks the value to tamper with")
	}
	r, err := Build("tampered.jsonl", tampered)
	if err != nil {
		t.Fatal(err)
	}
	if r.OK {
		t.Fatal("tampered journal reported OK")
	}
	var buf bytes.Buffer
	_ = r.WriteText(&buf)
	if !strings.Contains(buf.String(), "error  hash_mismatch  line 9") {
		t.Fatalf("text report does not name the tampered line:\n%s", buf.String())
	}
}

// A journal that ends with an orderly stop has no open gap; its end state is
// source.connection_closed. One that ends in an outage has an open gap.
func TestReportEndOfJournal(t *testing.T) {
	raw := loadFixture(t)
	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	stopped, err := Build("stopped", bytes.Join(lines[:12], []byte("\n")))
	if err != nil {
		t.Fatal(err)
	}
	s := stopped.Streams[0]
	if len(s.Gaps) != 1 || s.LastEvent != status.ConnectionClosed {
		t.Fatalf("orderly stop must not be an open gap: %+v %s", s.Gaps, s.LastEvent)
	}
	outage, err := Build("outage", bytes.Join(lines[:7], []byte("\n")))
	if err != nil {
		t.Fatal(err)
	}
	s = outage.Streams[0]
	if len(s.Gaps) != 1 || !s.Gaps[0].Open || s.LastEvent != status.ConnectionLost || s.LastSeen != "2026-11-02T10:00:09.500Z" {
		t.Fatalf("outage at the end must be an open gap: %+v %s %s", s.Gaps, s.LastEvent, s.LastSeen)
	}
}

func TestReportNamesRecurringAddressSpace(t *testing.T) {
	raw := loadFixture(t)
	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	// Re-use the fingerprint of generation 1 for generation 2 (verification
	// is irrelevant here; only the summary is checked).
	gen2 := bytes.Replace(lines[9], []byte(`"fingerprint":"`+evidence.Sum([]byte("gen2"))+`"`), []byte(`"fingerprint":"`+evidence.Sum([]byte("gen1"))+`"`), 1)
	if bytes.Equal(gen2, lines[9]) {
		t.Fatal("fixture line 10 is not the second inventory record")
	}
	lines[9] = gen2
	r, err := Build("recurring", bytes.Join(lines, []byte("\n")))
	if err != nil {
		t.Fatal(err)
	}
	if g := r.Streams[0].Generations; g[1].SameAs != 1 || g[0].SameAs != 0 {
		t.Fatalf("recurring fingerprint not named: %+v", g)
	}
}

// Journals written before the catalog alignment use provisional names; the
// report reads them under the catalog names.
func TestReportReadsProvisionalNames(t *testing.T) {
	raw := loadFixture(t)
	for old, now := range status.Provisional() {
		raw = bytes.ReplaceAll(raw, []byte(`"reason_code":"`+now+`"`), []byte(`"reason_code":"`+old+`"`))
	}
	r, err := Build("provisional", raw)
	if err != nil {
		t.Fatal(err)
	}
	s := r.Streams[0]
	if len(s.Gaps) != 2 || s.Gaps[0].ReasonCode != status.ConnectionLost || s.Gaps[1].Kind != "adapter" ||
		s.Generations[1].ReasonCode != status.NodeAdded || s.LastEvent != status.ConnectionEstablished {
		t.Fatalf("provisional names not translated: %+v %+v %s", s.Gaps, s.Generations, s.LastEvent)
	}
}

// A source.data.gap record is shown as a gap of kind subscription with the
// interval the producer declared.
func TestReportShowsDeclaredGap(t *testing.T) {
	raw := loadFixture(t)
	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	// Re-label the value record on line 9 as a declared gap; verification
	// is irrelevant for the summary.
	line := lines[8]
	line = bytes.Replace(line, []byte(`"event_type":"source.value.observed"`), []byte(`"event_type":"source.data.gap"`), 1)
	line = bytes.Replace(line, []byte(`"payload":{`), []byte(`"payload":{"from":"2026-11-02T10:00:26.000Z","to":"2026-11-02T10:00:29.500Z","cause":"source.subscription_stalled","reason_code":"common.gap",`), 1)
	lines[8] = line
	r, err := Build("declared", bytes.Join(lines, []byte("\n")))
	if err != nil {
		t.Fatal(err)
	}
	var found *Gap
	for i, g := range r.Streams[0].Gaps {
		if g.Kind == "subscription" {
			found = &r.Streams[0].Gaps[i]
		}
	}
	if found == nil || found.Duration != "3.5s" || found.ReasonCode != status.SubscriptionStalled {
		t.Fatalf("declared gap not reported: %+v", r.Streams[0].Gaps)
	}
}
