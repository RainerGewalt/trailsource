package opcua

import (
	"fmt"
	"path/filepath"

	"github.com/RainerGewalt/trailsource/internal/chain"
	"github.com/RainerGewalt/trailsource/internal/evidence"
	"github.com/RainerGewalt/trailsource/internal/journal"
)

func (a *Adapter) openEvidence(producer evidence.Producer) error {
	store := chain.FileStore{Path: filepath.Join(a.cfg.StateDir, a.cfg.Source.ID+".chain.json")}
	sealer, err := chain.New(store, producer, a.cfg.Source.ID)
	if err != nil {
		return fmt.Errorf("open chain state: %w", err)
	}
	a.sealer = sealer
	if !a.cfg.Journal.Enabled {
		return nil
	}
	j, err := journal.Open(a.cfg.Journal.Path)
	if err != nil {
		return fmt.Errorf("open journal: %w", err)
	}
	tail, err := j.LastRecord(a.cfg.Source.ID)
	if err == nil {
		a.recovery, err = sealer.Reconcile(tail)
	}
	if err != nil {
		_ = j.Close()
		return fmt.Errorf("reconcile chain state with journal: %w", err)
	}
	a.journal = j
	return nil
}
