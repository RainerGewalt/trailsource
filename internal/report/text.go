package report

import (
	"fmt"
	"io"
	"strings"

	"github.com/RainerGewalt/trailsource/internal/evidence"
)

// maxListed bounds the node lists and findings printed per section; the JSON
// form always carries everything.
const maxListed = 20

// VerifySummary renders the verification result in one line.
func (r *Report) VerifySummary() string {
	notices, errs := 0, 0
	for _, i := range r.Verify.Issues {
		if i.IsBreak() {
			errs++
		} else {
			notices++
		}
	}
	state := "OK"
	if !r.OK {
		state = "FAIL"
	}
	return fmt.Sprintf("%s: %d records in %d stream(s), %d error(s), %d notice(s)", state, r.Verify.Records, len(r.Verify.Streams), errs, notices)
}

// WriteText renders the report as plain text.
func (r *Report) WriteText(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "TrailSource report\n")
	fmt.Fprintf(&b, "Journal  %s\n", r.File)
	fmt.Fprintf(&b, "Verify   %s\n", r.VerifySummary())
	for _, s := range r.Streams {
		fmt.Fprintf(&b, "\nStream %s\n", s.StreamID)
		fmt.Fprintf(&b, "  Producer  %s\n", s.Producer)
		if s.Endpoint != "" {
			fmt.Fprintf(&b, "  Endpoint  %s\n", s.Endpoint)
		}
		fmt.Fprintf(&b, "  Period    %s .. %s (%s)\n", s.First, s.Last, s.Span)
		fmt.Fprintf(&b, "  At end    %s, last seen %s\n", s.LastEvent, orNone(s.LastSeen))
		fmt.Fprintf(&b, "  Records   %d\n", s.Records)
		for _, e := range s.Events {
			fmt.Fprintf(&b, "    %-28s %6d\n", e.EventType, e.Count)
		}
		if v := s.Values; v.Good+v.Uncertain+v.Bad > 0 {
			fmt.Fprintf(&b, "  Values    %d nodes; quality good %d, uncertain %d, bad %d; without source timestamp %d\n",
				v.Nodes, v.Good, v.Uncertain, v.Bad, v.NoSourceTimestamp)
		}

		fmt.Fprintf(&b, "\n  Address space generations (%d)\n", len(s.Generations))
		if len(s.Generations) == 0 {
			fmt.Fprintf(&b, "    none in this journal\n")
		}
		for _, g := range s.Generations {
			fmt.Fprintf(&b, "    gen %-3d seq %-6d %s  %-26s %d nodes  %s\n", g.Generation, g.Seq, g.RecordedAt, g.ReasonCode, g.NodeCount, ShortHash(g.Fingerprint))
			if g.ReasonCode != "source.inventory.initial" {
				fmt.Fprintf(&b, "            added %d, removed %d, changed %d\n", len(g.Added), len(g.Removed), len(g.Changed))
			}
			if g.Truncated {
				fmt.Fprintf(&b, "            truncated at max_nodes: the inventory covers a prefix of the address space\n")
			}
			listText(&b, "+", g.Added)
			listText(&b, "-", g.Removed)
			listText(&b, "~", g.Changed)
		}

		fmt.Fprintf(&b, "\n  Gaps (%d)\n", len(s.Gaps))
		if len(s.Gaps) == 0 {
			fmt.Fprintf(&b, "    none\n")
		}
		for _, g := range s.Gaps {
			end := g.End
			if g.Open {
				end = "(open)"
			}
			fmt.Fprintf(&b, "    %-10s %s .. %s  %s\n", g.Kind, g.Begin, end, g.Duration)
			if g.ReasonCode != "" || g.Reason != "" {
				fmt.Fprintf(&b, "               %s %s\n", g.ReasonCode, g.Reason)
			}
			if g.LastDataBefore != "" {
				fmt.Fprintf(&b, "               last value before the gap: %s\n", g.LastDataBefore)
			}
		}

		fmt.Fprintf(&b, "\n  Restarts (%d)\n", len(s.Restarts))
		for _, rs := range s.Restarts {
			anchor := "anchor " + ShortHash(rs.Anchor)
			if rs.AnchorGenesis {
				anchor = "anchor genesis"
			}
			after := ""
			if rs.AfterPrevious != "" {
				after = ", " + rs.AfterPrevious + " after the previous record"
			}
			fmt.Fprintf(&b, "    seq %-6d %s  %s%s\n", rs.Seq, rs.RecordedAt, anchor, after)
		}
	}

	fmt.Fprintf(&b, "\nFindings (%d)\n", len(r.Verify.Issues))
	for i, is := range r.Verify.Issues {
		if i == 2*maxListed {
			fmt.Fprintf(&b, "  ... %d more (use --json)\n", len(r.Verify.Issues)-i)
			break
		}
		fmt.Fprintf(&b, "  %-6s %-14s line %-6d seq %-6d %s\n", severity(is), is.Kind, is.Line, is.Seq, is.Detail)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func orNone(s string) string {
	if s == "" {
		return "never"
	}
	return s
}

func listText(b *strings.Builder, mark string, items []string) {
	for i, it := range items {
		if i == maxListed {
			fmt.Fprintf(b, "            %s ... %d more\n", mark, len(items)-i)
			return
		}
		fmt.Fprintf(b, "            %s %s\n", mark, it)
	}
}

func severity(i evidence.Issue) string {
	if i.IsBreak() {
		return "error"
	}
	return "notice"
}

// ShortHash shortens "sha256:<64 hex>" to its first 12 hex characters.
func ShortHash(h string) string {
	if strings.HasPrefix(h, evidence.HashPrefix) && len(h) > len(evidence.HashPrefix)+12 {
		return h[:len(evidence.HashPrefix)+12] + "..."
	}
	return h
}
