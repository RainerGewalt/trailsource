package chain

import (
	"fmt"
	"math"

	"github.com/RainerGewalt/trailsource/internal/evidence"
)

// Recovery records the observed disagreement, not its cause. An interrupted
// write, deletion or rollback can look identical. It is included in the next
// hashed restart payload; reconciliation never edits an existing journal.
type Recovery struct {
	Reason   string `json:"reason"`
	Previous State  `json:"previous_state"`
	Journal  State  `json:"journal_position"`
}

// Reconcile selects a verified, nonempty journal's tail as the next anchor.
// Call only before sealing the first record. An empty/moved journal supplies
// no new anchor: keeping the saved position preserves manual rotation.
// The recovered state is committed only together with the next restart record,
// so a crash during startup cannot erase the mismatch before it is recorded.
func (s *Sealer) Reconcile(tail *evidence.Record) (*Recovery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.restart || s.failed != nil {
		return nil, fmt.Errorf("chain reconciliation is only allowed at startup")
	}
	if tail == nil {
		return nil, nil
	}
	if tail.StreamID != s.stream {
		return nil, fmt.Errorf("journal belongs to stream %q, not %q", tail.StreamID, s.stream)
	}
	next := State{StreamID: s.stream, Seq: uint64(tail.Seq), ChainHash: tail.ChainHash, UpdatedAt: tail.Time.RecordedAt, Sealed: true}
	if s.state.Sealed && s.state.Seq == next.Seq && s.state.ChainHash == next.ChainHash {
		return nil, nil
	}
	if s.state.Sealed && s.state.Seq == next.Seq {
		return nil, fmt.Errorf("chain state and journal have different hashes at seq %d; preserve both files for investigation", next.Seq)
	}
	reason := "state_missing"
	if s.state.Sealed {
		reason = "state_behind_journal"
		if s.state.Seq > next.Seq {
			reason = "state_ahead_of_journal"
			if s.state.Seq == math.MaxUint64 {
				return nil, fmt.Errorf("saved chain sequence exhausted")
			}
			seq := s.state.Seq + 1
			s.nextSeq = &seq
		}
	}
	r := &Recovery{Reason: reason, Previous: s.state, Journal: next}
	s.state = next
	return r, nil
}
