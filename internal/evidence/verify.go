package evidence

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
)

// IssueKind classifies a verification finding. The values are the finding
// classes of the Trail Evidence Record specification (SPEC.md section 6.1 in
// the trail-evidence repository), so a TrailSource report and the reference
// verifier name the same problem the same way. Restarts are notices; every
// other kind is an error. Detail says which hash or link was affected.
type IssueKind string

const (
	IssueRestart          IssueKind = "restart"            // notice: chain_restart record, anchor consistent
	IssueLegacyIntegerSeq IssueKind = "legacy_integer_seq" // notice: seq written as a JSON integer (accepted during 0.x, as in the reference verifier)
	IssueSeqGap           IssueKind = "seq_gap"            // seq is not previous + 1 without chain_restart
	IssueHashMismatch     IssueKind = "hash_mismatch"      // recomputed content_hash or chain_hash differs
	IssueChainBreak       IssueKind = "chain_break"        // prev_hash or restart anchor does not link to the previous record
	IssueSchemaInvalid    IssueKind = "schema_invalid"     // unknown schema, malformed or unparsable record
)

// Issue is one verification finding.
type Issue struct {
	Kind     IssueKind `json:"kind"`
	StreamID string    `json:"stream_id,omitempty"`
	Line     int       `json:"line"`
	Seq      int64     `json:"seq"`
	Detail   string    `json:"detail"`
}

// IsBreak reports whether the issue is an integrity failure (as opposed to a
// notice: restart, legacy_integer_seq).
func (i Issue) IsBreak() bool {
	return i.Kind != IssueRestart && i.Kind != IssueLegacyIntegerSeq
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
			report.Issues = append(report.Issues, Issue{Kind: IssueSchemaInvalid, Line: lineNo, Seq: -1, Detail: err.Error()})
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

// seqOf returns seq as int64 for ordering and reporting, or -1 when it is
// invalid. Values beyond int64 are reported as invalid as well; a stream
// does not reach 2^63 records.
func seqOf(m map[string]any) int64 {
	n, form := ParseSeq(m["seq"])
	if form == SeqInvalid || n > 1<<63-1 {
		return -1
	}
	return int64(n)
}

func verifyStream(sid string, recs []parsed, issues *[]Issue) StreamReport {
	sr := StreamReport{StreamID: sid, FirstSeq: -1, LastSeq: -1}
	var prevHash string
	var prevSeq int64 = -1
	first := true

	integerSeqs := 0
	var firstInteger parsed
	add := func(kind IssueKind, p parsed, seq int64, detail string) {
		*issues = append(*issues, Issue{Kind: kind, StreamID: sid, Line: p.line, Seq: seq, Detail: detail})
		switch kind {
		case IssueLegacyIntegerSeq:
		case IssueRestart:
			sr.Restarts++
		case IssueSeqGap:
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
			add(IssueSchemaInvalid, p, seq, fmt.Sprintf("unsupported schema %q", s))
		}
		if _, form := ParseSeq(m["seq"]); form == SeqInteger {
			if integerSeqs == 0 {
				firstInteger = p
			}
			integerSeqs++
		}
		if seq < 0 {
			add(IssueSchemaInvalid, p, seq, "seq missing or not a canonical decimal string")
		}
		payload, hasPayload := m["payload"]
		if !hasPayload {
			add(IssueSchemaInvalid, p, seq, "payload missing")
		}

		// (2) content_hash
		gotContent, _ := m["content_hash"].(string)
		recomputedContent := gotContent
		if hasPayload {
			want, err := Hash(payload)
			if err != nil {
				add(IssueHashMismatch, p, seq, "content_hash: payload not canonicalisable: "+err.Error())
			} else {
				recomputedContent = want
				if want != gotContent {
					add(IssueHashMismatch, p, seq, fmt.Sprintf("content_hash recorded %s, recomputed %s", gotContent, want))
				}
			}
		}

		// (3) chain_hash over the core with the recomputed content_hash, as
		// the specification and the reference verifier do: an edited payload
		// therefore shows up in both hashes of its own line.
		gotChain, _ := m["chain_hash"].(string)
		core := coreFromMap(m)
		core["content_hash"] = recomputedContent
		wantChain, err := Hash(core)
		if err != nil {
			add(IssueHashMismatch, p, seq, "chain_hash: core not canonicalisable: "+err.Error())
		} else if wantChain != gotChain {
			add(IssueHashMismatch, p, seq, fmt.Sprintf("chain_hash recorded %s, recomputed %s", gotChain, wantChain))
		}

		// (4) and (5) linkage
		prev, _ := m["prev_hash"].(string)
		restart, _ := m["chain_restart"].(bool)
		anchor, _ := m["anchor_hash"].(string)
		switch {
		case restart:
			if !IsHash(anchor) {
				add(IssueSchemaInvalid, p, seq, "chain_restart without anchor_hash")
			} else if anchor != prev {
				add(IssueSchemaInvalid, p, seq, "chain_restart with prev_hash != anchor_hash")
			} else if !first && prevHash != "" && anchor != prevHash {
				// The previous segment is in this journal, so the anchor must
				// point at its last chain_hash (same rule as the reference verifier).
				add(IssueChainBreak, p, seq, fmt.Sprintf("anchor_hash %s does not match the last chain_hash %s of the previous segment", anchor, prevHash))
			} else {
				detail := fmt.Sprintf("restart at seq %d, anchor %s", seq, anchor)
				if first {
					detail += " (previous segment not in this journal)"
				}
				add(IssueRestart, p, seq, detail)
			}
		case first:
			if prev != GenesisHash {
				add(IssueChainBreak, p, seq, fmt.Sprintf("first record prev_hash %s is not genesis and not a restart", prev))
			}
		default:
			if prev != prevHash {
				add(IssueChainBreak, p, seq, fmt.Sprintf("prev_hash %s does not match previous chain_hash %s", prev, prevHash))
			}
			if seq != prevSeq+1 {
				add(IssueSeqGap, p, seq, fmt.Sprintf("seq %d follows %d", seq, prevSeq))
			}
		}

		prevHash = gotChain
		prevSeq = seq
		first = false
	}
	if integerSeqs > 0 {
		add(IssueLegacyIntegerSeq, firstInteger, seqOf(firstInteger.m), fmt.Sprintf(
			"%d record(s) write seq as a JSON integer, the form of journals written before seq became a decimal string; accepted, hashes are checked as written", integerSeqs))
	}
	sr.LastChain = prevHash
	return sr
}
