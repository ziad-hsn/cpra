package systems

import (
	"context"
	"errors"

	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
)

// loadCatalogBatches amortizes synchronous Raft commit latency during startup.
// This is still single-owner initialization before any provider may execute.
// Private inputs are capped at 8 MiB per batch (one individually validated
// preparation may be larger, up to PrepareRuntime's 32 MiB closure limit).
func (s *DurableSystem) loadCatalogBatches(ctx context.Context, view management.ReadView) error {
	const privateBatchBytes = 8 << 20
	maxCommands, maxBytes := s.store.CommandLimits()
	maxCommands = min(maxCommands, 100)
	var pending []catalogProjection
	var commands []persistence.Command
	encoded, private := 64, 0
	closePending := func() {
		for _, p := range pending {
			_ = p.prepared.Close()
		}
		clear(pending)
		pending = pending[:0]
		clear(commands)
		commands = commands[:0]
		encoded, private = 64, 0
	}
	defer closePending()
	flush := func() error {
		if len(commands) == 0 {
			return nil
		}
		results, err := s.store.Submit(ctx, commands)
		if err != nil {
			return err
		}
		if len(results) != len(pending) {
			return management.ErrOutcomeUnconfirmed
		}
		for n, result := range results {
			if result.Err != nil {
				return result.Err
			}
			if result.Monitor == nil {
				return errors.New("committed startup monitor has no projection")
			}
			if err := s.installCatalogProjection(ctx, pending[n], *result.Monitor); err != nil {
				return err
			}
			s.catalogRuntime.known[result.Monitor.ID] = true
		}
		closePending()
		return nil
	}
	for after := ""; ; {
		rows, next, err := view.Page(ctx, "Monitor", after, 100)
		if err != nil {
			return err
		}
		for _, row := range rows {
			p, err := s.catalogRuntime.prepare(ctx, view, row.Metadata.ID)
			if err != nil {
				return err
			}
			command := s.catalogConfigureCommand(p)
			bound, err := persistence.CommandEncodedBound(command)
			if err != nil || bound+65 > maxBytes {
				_ = p.prepared.Close()
				return errors.New("monitor dependency guard exceeds durable command capacity")
			}
			if len(pending) > 0 && (len(pending) >= maxCommands || encoded+bound+1 > maxBytes || private+p.runtime.PreparationBytes > privateBatchBytes) {
				if err := flush(); err != nil {
					_ = p.prepared.Close()
					return err
				}
			}
			pending = append(pending, p)
			commands = append(commands, command)
			encoded += bound + 1
			private += p.runtime.PreparationBytes
		}
		if next == "" {
			break
		}
		after = next
	}
	return flush()
}
