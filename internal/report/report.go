// Package report turns a JSON Lines journal into a readable account per
// stream: period, records per event type, address-space generations with
// their diff, connection and adapter gaps with duration, restarts, and the
// verification result. It reads journals; it never changes them.
package report

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/RainerGewalt/trailsource/internal/evidence"
	"github.com/RainerGewalt/trailsource/internal/status"
)

// Report is the rendered content of one journal.
type Report struct {
	File    string          `json:"file"`
	Verify  evidence.Report `json:"verify"`
	OK      bool            `json:"ok"`
	Streams []Stream        `json:"streams"`
}

// Stream summarises one hash chain.
type Stream struct {
	StreamID    string       `json:"stream_id"`
	Producer    string       `json:"producer"`
	Endpoint    string       `json:"endpoint,omitempty"`
	First       string       `json:"first"`
	Last        string       `json:"last"`
	Span        string       `json:"span"`
	Records     int          `json:"records"`
	LastEvent   string       `json:"last_event"` // state at the end of the journal (docs/STATUS.md)
	LastSeen    string       `json:"last_seen,omitempty"`
	Events      []EventCount `json:"events"`
	Values      ValueStats   `json:"values"`
	Generations []Generation `json:"generations"`
	Gaps        []Gap        `json:"gaps"`
	Restarts    []Restart    `json:"restarts"`
}

// EventCount is the number of records of one event type.
type EventCount struct {
	EventType string `json:"event_type"`
	Count     int    `json:"count"`
}

// ValueStats summarises source.value.observed records.
type ValueStats struct {
	Good              int `json:"good"`
	Uncertain         int `json:"uncertain"`
	Bad               int `json:"bad"`
	NoSourceTimestamp int `json:"no_source_timestamp"`
	Nodes             int `json:"nodes"`
}

// Generation is one address-space generation (one inventory record).
type Generation struct {
	Generation          int64    `json:"generation"`
	Seq                 int64    `json:"seq"`
	Line                int      `json:"line"`
	RecordedAt          string   `json:"recorded_at"`
	ReasonCode          string   `json:"reason_code"`
	NodeCount           int      `json:"node_count"`
	Fingerprint         string   `json:"fingerprint"`
	PreviousFingerprint string   `json:"previous_fingerprint,omitempty"`
	Truncated           bool     `json:"truncated,omitempty"`
	SameAs              int64    `json:"same_as,omitempty"` // earlier generation with the same fingerprint (paths may still differ)
	Added               []string `json:"added"`
	Removed             []string `json:"removed"`
	Changed             []string `json:"changed"`
}

// Gap is a period without data from the source.
type Gap struct {
	Kind           string `json:"kind"` // "connection" or "adapter"
	Begin          string `json:"begin"`
	End            string `json:"end,omitempty"`
	Duration       string `json:"duration"`
	Open           bool   `json:"open,omitempty"` // no reconnect before the journal ends
	ReasonCode     string `json:"reason_code,omitempty"`
	Reason         string `json:"reason,omitempty"`
	LastDataBefore string `json:"last_data_before,omitempty"`
	BeginSeq       int64  `json:"begin_seq"`
	EndSeq         int64  `json:"end_seq,omitempty"`
}

// Restart is a record with chain_restart.
type Restart struct {
	Seq           int64  `json:"seq"`
	Line          int    `json:"line"`
	RecordedAt    string `json:"recorded_at"`
	Anchor        string `json:"anchor"`
	AnchorGenesis bool   `json:"anchor_genesis"`
	AfterPrevious string `json:"after_previous,omitempty"` // time since the previous record of the stream
}

// FromFile builds the report of a journal file.
func FromFile(path string) (*Report, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Build(path, raw)
}

// Build builds the report of journal bytes; name is only used for display.
func Build(name string, journal []byte) (*Report, error) {
	ver, err := evidence.VerifyJournal(bytes.NewReader(journal))
	if err != nil {
		return nil, err
	}
	streams, order, err := readEntries(bytes.NewReader(journal))
	if err != nil {
		return nil, err
	}
	r := &Report{File: name, Verify: ver, OK: ver.OK()}
	for _, sid := range order {
		r.Streams = append(r.Streams, summarise(sid, streams[sid]))
	}
	return r, nil
}

// entry is the part of a record the report needs.
type entry struct {
	line       int
	seq        int64
	eventType  string
	producer   string
	endpoint   string
	recordedAt string
	readAt     string
	restart    bool
	anchor     string
	payload    json.RawMessage
}

type wireRecord struct {
	StreamID     string          `json:"stream_id"`
	Seq          json.RawMessage `json:"seq"`
	EventType    string          `json:"event_type"`
	ChainRestart bool            `json:"chain_restart"`
	AnchorHash   string          `json:"anchor_hash"`
	Producer     struct {
		ID string `json:"id"`
	} `json:"producer"`
	Time struct {
		RecordedAt string `json:"recorded_at"`
		ReadAt     string `json:"read_at"`
	} `json:"time"`
	Payload json.RawMessage `json:"payload"`
	Context struct {
		Endpoint string `json:"endpoint"`
	} `json:"context"`
}

func readEntries(r io.Reader) (map[string][]entry, []string, error) {
	streams := map[string][]entry{}
	var order []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		b := bytes.TrimSpace(sc.Bytes())
		if len(b) == 0 {
			continue
		}
		var w wireRecord
		if err := json.Unmarshal(b, &w); err != nil {
			continue // verify reports unparsable lines
		}
		seq := seqFromRaw(w.Seq)
		e := entry{line: line, seq: seq, eventType: w.EventType, producer: w.Producer.ID, endpoint: w.Context.Endpoint,
			recordedAt: w.Time.RecordedAt, readAt: w.Time.ReadAt, restart: w.ChainRestart, anchor: w.AnchorHash, payload: w.Payload}
		if _, seen := streams[w.StreamID]; !seen {
			order = append(order, w.StreamID)
		}
		streams[w.StreamID] = append(streams[w.StreamID], e)
	}
	return streams, order, sc.Err()
}

type connectionPayload struct {
	State      string `json:"state"`
	ReasonCode string `json:"reason_code"`
	Reason     string `json:"reason"`
}

type inventoryNode struct {
	NodeID     string `json:"node_id"`
	BrowsePath string `json:"browse_path"`
	DataType   string `json:"data_type"`
}

type inventoryPayload struct {
	ReasonCode          string          `json:"reason_code"`
	ChangeKind          string          `json:"change_kind"`
	Generation          int64           `json:"generation"`
	NodeCount           int             `json:"node_count"`
	Fingerprint         string          `json:"fingerprint"`
	PreviousFingerprint *string         `json:"previous_fingerprint"`
	Truncated           bool            `json:"truncated"`
	Nodes               []inventoryNode `json:"nodes"`
	Diff                struct {
		Added   []inventoryNode `json:"added"`
		Removed []inventoryNode `json:"removed"`
		Changed []struct {
			NodeID string `json:"node_id"`
			Field  string `json:"field"`
			Old    string `json:"old"`
			New    string `json:"new"`
		} `json:"changed"`
	} `json:"diff"`
}

type valuePayload struct {
	Quality         string `json:"quality"`
	SourceTimestamp any    `json:"source_timestamp"`
	EntityKey       string `json:"entity_key"`
	NativeNodeID    string `json:"native_node_id"`
}

func summarise(sid string, entries []entry) Stream {
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].seq < entries[j].seq })
	s := Stream{StreamID: sid, Records: len(entries)}
	counts := map[string]int{}
	nodes := map[string]bool{}
	var open *Gap
	var lastValueAt, prevRecordAt string
	seenAt := func(e entry) string {
		if e.readAt != "" {
			return e.readAt
		}
		return e.recordedAt
	}
	var first, last time.Time

	for i, e := range entries {
		counts[e.eventType]++
		if s.Producer == "" {
			s.Producer = e.producer
		}
		if s.Endpoint == "" {
			s.Endpoint = e.endpoint
		}
		if t, ok := parseTime(e.recordedAt); ok {
			if first.IsZero() || t.Before(first) {
				first = t
				s.First = e.recordedAt
			}
			if t.After(last) {
				last = t
				s.Last = e.recordedAt
			}
		}

		if e.restart {
			rs := Restart{Seq: e.seq, Line: e.line, RecordedAt: e.recordedAt, Anchor: e.anchor, AnchorGenesis: e.anchor == evidence.GenesisHash}
			if i > 0 {
				rs.AfterPrevious = between(prevRecordAt, e.recordedAt)
				if open == nil {
					// The previous process left no closing record: it was
					// killed or crashed. The silence still is a gap.
					open = &Gap{Kind: "adapter", Begin: prevRecordAt, BeginSeq: entries[i-1].seq,
						Reason: "no closing record before the restart (process killed or crashed)", LastDataBefore: lastValueAt}
				}
			}
			s.Restarts = append(s.Restarts, rs)
			s.LastEvent = status.Restart
		}

		switch e.eventType {
		case status.EventBridgeRestarted:
			var c connectionPayload
			_ = json.Unmarshal(e.payload, &c)
			if code, ok := status.Canonical(c.ReasonCode); ok {
				s.LastEvent = code
			}
		case status.EventValueObserved:
			var v valuePayload
			_ = json.Unmarshal(e.payload, &v)
			switch v.Quality {
			case "good":
				s.Values.Good++
			case "uncertain":
				s.Values.Uncertain++
			case "bad":
				s.Values.Bad++
			}
			if v.SourceTimestamp == nil {
				s.Values.NoSourceTimestamp++
			}
			s.LastSeen = seenAt(e)
			key := v.EntityKey
			if key == "" {
				key = v.NativeNodeID
			}
			nodes[key] = true
			lastValueAt = e.readAt
			if lastValueAt == "" {
				lastValueAt = e.recordedAt
			}
		case status.EventConnectionChanged:
			var c connectionPayload
			_ = json.Unmarshal(e.payload, &c)
			code, ok := status.Canonical(c.ReasonCode)
			if !ok {
				code = legacyConnectionCode(c)
			}
			s.LastEvent = code
			if c.State == "connected" {
				s.LastSeen = seenAt(e)
				if open != nil {
					open.End, open.EndSeq = e.recordedAt, e.seq
					open.Duration = between(open.Begin, open.End)
					s.Gaps = append(s.Gaps, *open)
					open = nil
				}
			} else if open == nil {
				kind := "connection"
				if code == status.ConnectionClosed {
					kind = "adapter"
				}
				open = &Gap{Kind: kind, Begin: e.recordedAt, BeginSeq: e.seq, ReasonCode: code, Reason: c.Reason, LastDataBefore: lastValueAt}
			} else if open.ReasonCode == "" {
				open.ReasonCode, open.Reason = code, c.Reason
			}
		case status.EventInventoryChanged:
			g := generation(e)
			for _, earlier := range s.Generations {
				if earlier.Fingerprint == g.Fingerprint && g.Fingerprint != "" {
					g.SameAs = earlier.Generation
					break
				}
			}
			s.Generations = append(s.Generations, g)
			s.LastEvent, s.LastSeen = g.ReasonCode, seenAt(e)
		}
		prevRecordAt = e.recordedAt
	}
	// An orderly stop at the end of the journal is where the recording ends,
	// not a gap; LastEvent says so.
	if open != nil && !(open.Kind == "adapter" && open.ReasonCode == status.ConnectionClosed) {
		open.Open = true
		open.Duration = "at least " + between(open.Begin, s.Last)
		s.Gaps = append(s.Gaps, *open)
	}
	s.Values.Nodes = len(nodes)
	s.Span = between(s.First, s.Last)
	for k, v := range counts {
		s.Events = append(s.Events, EventCount{EventType: k, Count: v})
	}
	sort.Slice(s.Events, func(i, j int) bool {
		if s.Events[i].Count == s.Events[j].Count {
			return s.Events[i].EventType < s.Events[j].EventType
		}
		return s.Events[i].Count > s.Events[j].Count
	})
	return s
}

func generation(e entry) Generation {
	var p inventoryPayload
	_ = json.Unmarshal(e.payload, &p)
	g := Generation{Generation: p.Generation, Seq: e.seq, Line: e.line, RecordedAt: e.recordedAt,
		NodeCount: p.NodeCount, Fingerprint: p.Fingerprint, Truncated: p.Truncated,
		Added: []string{}, Removed: []string{}, Changed: []string{}}
	if p.PreviousFingerprint != nil {
		g.PreviousFingerprint = *p.PreviousFingerprint
	}
	if code, ok := status.Canonical(p.ReasonCode); ok {
		g.ReasonCode = code
	} else {
		g.ReasonCode = status.Inventory(p.ChangeKind == "initial", len(p.Diff.Added), len(p.Diff.Removed), len(p.Diff.Changed))
	}
	for _, n := range p.Diff.Added {
		g.Added = append(g.Added, describeNode(n))
	}
	for _, n := range p.Diff.Removed {
		g.Removed = append(g.Removed, describeNode(n))
	}
	paths := map[string]string{}
	for _, n := range p.Nodes {
		paths[n.NodeID] = n.BrowsePath
	}
	for _, c := range p.Diff.Changed {
		name := paths[c.NodeID]
		if name == "" {
			name = c.NodeID
		}
		g.Changed = append(g.Changed, fmt.Sprintf("%s: %s %s -> %s", name, c.Field, c.Old, c.New))
	}
	return g
}

func describeNode(n inventoryNode) string {
	name := n.BrowsePath
	if name == "" {
		name = n.NodeID
	}
	if n.DataType != "" {
		return name + " (" + n.DataType + ")"
	}
	return name
}

// legacyConnectionCode derives a reason code for connection records written
// before TrailSource wrote reason codes at all.
func legacyConnectionCode(c connectionPayload) string {
	if c.State == "connected" {
		return status.ConnectionEstablished
	}
	if c.Reason == "shutdown" {
		return status.ConnectionClosed
	}
	return status.ConnectionLost
}

// seqFromRaw reads seq in either form (decimal string, or the JSON integer
// of older journals); -1 when invalid. Verification reports the details.
func seqFromRaw(raw json.RawMessage) int64 {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return -1
	}
	n, form := evidence.ParseSeq(v)
	if form == evidence.SeqInvalid || n > 1<<63-1 {
		return -1
	}
	return int64(n)
}

func parseTime(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, s)
	return t, err == nil
}

func between(a, b string) string {
	ta, okA := parseTime(a)
	tb, okB := parseTime(b)
	if !okA || !okB {
		return ""
	}
	return tb.Sub(ta).Round(time.Millisecond).String()
}
