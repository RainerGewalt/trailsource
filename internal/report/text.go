package report

import (
	"fmt"
	"io"
	"strings"

	"github.com/RainerGewalt/trailsource/internal/evidence"
	"github.com/RainerGewalt/trailsource/internal/status"
)

// maxListed bounds the node lists and findings printed per section; the JSON
// form always carries everything.
const maxListed = 20

// VerifySummary renders the verification result in one line.
func (r *Report) VerifySummary() string {
	if r.Empty {
		return "EMPTY: no records in this journal, nothing was verified"
	}
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

// Scope is what verification covers and what it does not, in the words of
// the README, "What it does not do". Every report ends with it.
const Scope = "What verification covers: every record's hashes recompute and each record links to the one before it, " +
	"so no record was edited, removed, inserted or reordered without re-sealing everything after it. " +
	"What it does not cover: whoever controls the file can rewrite and re-seal all of it; records cut off at the end of the journal " +
	"are not detectable without an anchor outside it; the context fields of the records (site, line, device, host) are not covered " +
	"by any hash, so this report does not show them; and nothing in a journal shows that the values were true."

// Verdict explains the verification result above the details.
func (r *Report) Verdict() string {
	switch {
	case r.Empty:
		return "The journal holds no records, so there is nothing to verify; an empty file proves nothing. " +
			"Check journal.enabled and journal.path in the adapter's configuration."
	case !r.OK:
		return "FAIL means the journal is not what was sealed: the findings name every line whose hashes or links do not hold. " +
			"One cause besides an edit is known: a chain_break at the first record after a restart can also come from an adapter " +
			"that stopped between saving its chain position and writing that record; verification alone cannot tell the two apart."
	default:
		return "Every record in this file verifies and links to the one before it."
	}
}

// WriteText renders the report as plain text.
func (r *Report) WriteText(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "TrailSource report\n")
	fmt.Fprintf(&b, "Journal  %s\n", r.File)
	if r.Generator != "" || r.GeneratedAt != "" {
		fmt.Fprintf(&b, "Report   trailsource %s, %s\n", orNone(r.Generator), orNone(r.GeneratedAt))
	}
	fmt.Fprintf(&b, "Verify   %s\n", r.VerifySummary())
	wrap(&b, "         ", r.Verdict())
	if !r.OK && !r.Empty {
		writeFindingsText(&b, r.Verify.Issues)
	}
	for _, s := range r.Streams {
		fmt.Fprintf(&b, "\nStream %s\n", s.StreamID)
		fmt.Fprintf(&b, "  Producer  %s, version %s\n", s.Producer, strings.Join(s.ProducerVersions, ", "))
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
			if g.ReasonCode != status.InventoryInitial {
				fmt.Fprintf(&b, "            added %d, removed %d, changed %d\n", len(g.Added), len(g.Removed), len(g.Changed))
			}
			if g.SameAs != 0 {
				fmt.Fprintf(&b, "            same fingerprint as generation %d\n", g.SameAs)
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

	if r.OK || r.Empty {
		writeFindingsText(&b, r.Verify.Issues)
	}
	b.WriteString("\n")
	wrap(&b, "", Scope)
	_, err := io.WriteString(w, b.String())
	return err
}

func writeFindingsText(b *strings.Builder, issues []evidence.Issue) {
	fmt.Fprintf(b, "\nFindings (%d)\n", len(issues))
	for i, is := range issues {
		if i == 2*maxListed {
			fmt.Fprintf(b, "  ... %d more (use --json)\n", len(issues)-i)
			break
		}
		fmt.Fprintf(b, "  %-6s %-14s line %-6d seq %-6d %s\n", severity(is), is.Kind, is.Line, is.Seq, is.Detail)
	}
}

// wrap writes text in lines of at most 78 characters, each with prefix.
func wrap(b *strings.Builder, prefix, text string) {
	line := prefix
	for _, word := range strings.Fields(text) {
		if len(line) > len(prefix) && len(line)+1+len(word) > 78 {
			b.WriteString(line + "\n")
			line = prefix
		}
		if len(line) > len(prefix) {
			line += " "
		}
		line += word
	}
	if len(line) > len(prefix) {
		b.WriteString(line + "\n")
	}
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
