package evidence

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

// IssueKind classifies a verification finding. Restarts are informational,
// gaps and breaks are failures of different nature and are reported apart.
type IssueKind string

const (
	IssueRestart     IssueKind = "restart"      // chain_restart record; prev_hash == anchor_hash checked
	IssueGap         IssueKind = "gap"          // seq is not previous + 1 without chain_restart
	IssueContentHash IssueKind = "content_hash" // recomputed content_hash differs
	IssueChainHash   IssueKind = "chain_hash"   // recomputed chain_hash differs
	IssuePrevHash    IssueKind = "prev_hash"    // prev_hash does not link to the previous record
	IssueSchema      IssueKind = "schema"       // unknown schema or malformed record
	IssueAnchorHash  IssueKind = "anchor_hash"  // chain_restart without consistent anchor
	IssueUnparsable  IssueKind = "unparsable"   // line is not a JSON object
)

// Issue is one verification finding.
type Issue struct {
	Kind     IssueKind `json:"kind"`
	StreamID string    `json:"stream_id,omitempty"`
	Line     int       `json:"line"`
	Seq      int64     `json:"seq"`
	Detail   string    `json:"detail"`
}

// IsBreak reports whether the issue is an integrity failure (as opposed to an
// informational restart).
func (i Issue) IsBreak() bool {
	return i.Kind != IssueRestart
}

// StreamReport summarises one stream.
type StreamReport struct {
	StreamID  string `json:"stream_id"`
	Records   int    `json:"records"`
	FirstSeq  int64  `json:"first_seq"`
	LastSeq   int64  `json:"last_seq"`
	Restarts  int    `json:"restarts"`
	Gaps      int    `json:"gaps"`
	Breaks    int    `json:"breaks"`
	LastChain string `json:"last_chain_hash"`
}

// Report is the result of verifying a journal.
type Report struct {
	Records int            `json:"records"`
	Streams []StreamReport `json:"streams"`
	Issues  []Issue        `json:"issues"`
}

// OK reports whether no gap or hash break was found.
func (r Report) OK() bool {
	for _, i := range r.Issues {
		if i.IsBreak() {
			return false
		}
	}
	return true
}

type parsed struct {
	line int
	m    map[string]any
	raw  []byte
}

// VerifyJournal reads JSON Lines from r and verifies every stream found in it.
// Records are processed in seq order per stream. The file order is not required
// to match seq order, but a journal written by a single adapter will match.
func VerifyJournal(r io.Reader) (Report, error) {
	var report Report
	streams := map[string][]parsed{}
	var order []string

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		raw := append([]byte(nil), line...)
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			report.Issues = append(report.Issues, Issue{Kind: IssueUnparsable, Line: lineNo, Seq: -1, Detail: err.Error()})
			continue
		}
		report.Records++
		sid, _ := m["stream_id"].(string)
		if _, seen := streams[sid]; !seen {
			order = append(order, sid)
		}
		streams[sid] = append(streams[sid], parsed{line: lineNo, m: m, raw: raw})
	}
	if err := sc.Err(); err != nil {
		return report, err
	}

	for _, sid := range order {
		recs := streams[sid]
		sort.SliceStable(recs, func(i, j int) bool { return seqOf(recs[i].m) < seqOf(recs[j].m) })
		sr := verifyStream(sid, recs, &report.Issues)
		report.Streams = append(report.Streams, sr)
	}
	return report, nil
}

func seqOf(m map[string]any) int64 {
	n, ok := m["seq"].(json.Number)
	if !ok {
		return -1
	}
	v, err := n.Int64()
	if err != nil {
		return -1
	}
	return v
}

func verifyStream(sid string, recs []parsed, issues *[]Issue) StreamReport {
	sr := StreamReport{StreamID: sid, FirstSeq: -1, LastSeq: -1}
	var prevHash string
	var prevSeq int64 = -1
	first := true

	add := func(kind IssueKind, p parsed, seq int64, detail string) {
		*issues = append(*issues, Issue{Kind: kind, StreamID: sid, Line: p.line, Seq: seq, Detail: detail})
		switch kind {
		case IssueRestart:
			sr.Restarts++
		case IssueGap:
			sr.Gaps++
		default:
			sr.Breaks++
		}
	}

	for _, p := range recs {
		m := p.m
		seq := seqOf(m)
		sr.Records++
		if first {
			sr.FirstSeq = seq
		}
		sr.LastSeq = seq

		// (1) schema and shape
		if s, _ := m["schema"].(string); s != Schema {
			add(IssueSchema, p, seq, fmt.Sprintf("unsupported schema %q", s))
		}
		if seq < 0 {
			add(IssueSchema, p, seq, "seq missing or not a non-negative integer")
		}
		payload, hasPayload := m["payload"]
		if !hasPayload {
			add(IssueSchema, p, seq, "payload missing")
		}

		// (2) content_hash
		gotContent, _ := m["content_hash"].(string)
		if hasPayload {
			want, err := Hash(payload)
			if err != nil {
				add(IssueContentHash, p, seq, "payload not canonicalisable: "+err.Error())
			} else if want != gotContent {
				add(IssueContentHash, p, seq, fmt.Sprintf("content_hash %s, recomputed %s", gotContent, want))
			}
		}

		// (3) chain_hash
		gotChain, _ := m["chain_hash"].(string)
		wantChain, err := Hash(coreFromMap(m))
		if err != nil {
			add(IssueChainHash, p, seq, "core not canonicalisable: "+err.Error())
		} else if wantChain != gotChain {
			add(IssueChainHash, p, seq, fmt.Sprintf("chain_hash %s, recomputed %s", gotChain, wantChain))
		}

		// (4) and (5) linkage
		prev, _ := m["prev_hash"].(string)
		restart, _ := m["chain_restart"].(bool)
		anchor, _ := m["anchor_hash"].(string)
		switch {
		case restart:
			if !IsHash(anchor) {
				add(IssueAnchorHash, p, seq, "chain_restart without anchor_hash")
			} else if anchor != prev {
				add(IssueAnchorHash, p, seq, "chain_restart with prev_hash != anchor_hash")
			} else {
				detail := fmt.Sprintf("restart at seq %d, anchor %s", seq, anchor)
				if !first && prevHash != "" && anchor != prevHash {
					detail += fmt.Sprintf(" (previous record in file has chain_hash %s)", prevHash)
				}
				add(IssueRestart, p, seq, detail)
			}
		case first:
			if prev != GenesisHash {
				add(IssuePrevHash, p, seq, fmt.Sprintf("first record prev_hash %s is not genesis and not a restart", prev))
			}
		default:
			if prev != prevHash {
				add(IssuePrevHash, p, seq, fmt.Sprintf("prev_hash %s does not match previous chain_hash %s", prev, prevHash))
			}
			if seq != prevSeq+1 {
				add(IssueGap, p, seq, fmt.Sprintf("seq %d follows %d", seq, prevSeq))
			}
		}

		prevHash = gotChain
		prevSeq = seq
		first = false
	}
	sr.LastChain = prevHash
	return sr
}
