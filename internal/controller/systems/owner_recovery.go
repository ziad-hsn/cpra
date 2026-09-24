package systems

import (
	"context"
	"errors"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/persistence"
)

var errAdmissionRecovering = errors.New("controller is reconciling committed work")

// Only the ECS owner submits these writes. Keeping the submission boundary
// separate also permits lost-reply tests against the real state machine.
type ownerCommitter interface {
	Submit(context.Context, []persistence.Command) ([]persistence.Result, error)
	Flush(context.Context) error
}

type ownerWriteKind uint8

const (
	resultWrite ownerWriteKind = iota
	sloWrite
	catalogWrite
	expiryWrite
)

// At most one write and one received result suffix belong to the owner. No ECS
// pointers are retained: a Raft reply can arrive after a structural world edit.
// Commands, preparations and timestamps remain unchanged across uncertain replies.
type ownerWrite struct {
	kind         ownerWriteKind
	commands     []persistence.Command
	observations []jobs.Result
	monitorUIDs  []string
	results      []persistence.Result
	confirmed    bool
	prepared     bool
	projection   *catalogProjection
	installed    bool
	expiries     []ecs.Entity
	completed    int
}

func (s *DurableSystem) writer() ownerCommitter {
	if s.committer != nil {
		return s.committer
	}
	return s.store
}

func (s *DurableSystem) beginOwnerWrite(p *ownerWrite) bool {
	if s.pendingWrite != nil {
		s.fail(errors.New("controller attempted a write before resolving its previous submission"))
		return false
	}
	s.pendingWrite = p
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.advanceOwnerWrite(ctx); err != nil {
		s.recoveryError(err, ctx)
		return false
	}
	return true
}

func (s *DurableSystem) advanceOwnerWrite(ctx context.Context) error {
	p := s.pendingWrite
	if p == nil {
		return nil
	}
	if p.kind == catalogWrite && !p.prepared {
		if err := s.prepareCatalogWrite(ctx, p); err != nil {
			return err
		}
		p.prepared = true
	}
	if !p.confirmed {
		results, err := s.writer().Submit(ctx, p.commands)
		if err != nil {
			return err
		}
		if len(results) != len(p.commands) {
			return persistence.ErrCommitUnconfirmed
		}
		p.results, p.confirmed = results, true
	}
	switch p.kind {
	case resultWrite:
		if err := s.finishResultWrite(ctx, p); err != nil {
			return err
		}
	case sloWrite:
		if p.results[0].Err != nil {
			return p.results[0].Err
		}
		s.lastSLO = p.commands[0].At
	case catalogWrite:
		if err := s.finishCatalogWrite(ctx, p); err != nil {
			return err
		}
	case expiryWrite:
		if err := s.finishExpiryWrite(ctx, p); err != nil {
			return err
		}
	default:
		return errors.New("unknown controller pending write")
	}
	s.pendingWrite = nil
	return nil
}

// Recovery never clears stopping or lastError. The FIFO barrier settles any
// earlier submission before the original commands may be replayed. Each owner
// tick has a finite recovery budget, and no helper goroutine owns a second write.
func (s *DurableSystem) recoverWrites(now time.Time) bool {
	if now.Before(s.nextRecovery) {
		return false
	}
	s.nextRecovery = now.Add(100 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if err := s.writer().Flush(ctx); err != nil {
		s.recoveryError(err, ctx)
		return false
	}
	status, err := s.store.RetryReturnedExecutions(ctx)
	if err != nil {
		s.recoveryError(err, ctx)
		return false
	}
	if status.Returned != 0 {
		return false
	}
	if err := s.advanceOwnerWrite(ctx); err != nil {
		s.recoveryError(err, ctx)
		return false
	}
	return true
}

func (s *DurableSystem) recoveryError(err error, ctx context.Context) {
	// An expired bounded recovery attempt remains unresolved. It is not evidence
	// of success, nor permission to generate a replacement command.
	if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		s.recovering.Store(true)
		return
	}
	s.fail(err)
}

func (s *DurableSystem) resultsBuffered() bool {
	if len(s.resultBacklog) != 0 {
		return true
	}
	for _, ch := range s.results {
		if len(ch) != 0 {
			return true
		}
	}
	return false
}

func (s *DurableSystem) releaseResult(r jobs.Result) {
	delete(s.admitted, r.ActionID)
	if r.Type == "pulse" && s.pendingCheckID[r.MonitorID] == r.ID {
		delete(s.pending, r.MonitorID)
		delete(s.pendingCheckID, r.MonitorID)
	}
}

// A check withheld before provider invocation contributes no target failure.
// Keep its original scheduled obligation instead of creating a new SLO sample.
func (s *DurableSystem) requeueWithheldCheck(r jobs.Result) {
	s.releaseResult(r)
	ent, ok := s.entities[r.MonitorID]
	if !ok || !s.world.Alive(ent) || s.states.Get(ent).Revision != r.Revision || s.disabled.Get(ent) != nil || s.isSnoozed(ent) {
		return
	}
	if p, ok := s.managed[r.MonitorID]; ok && p.version != r.ProjectionVersion {
		return
	}
	s.checks[ent.ID()].ready = r.Scheduled.UnixNano()
	s.scheduler.Requeue([]ecs.Entity{ent})
	s.states.Get(ent).SetPulsePending(false)
	s.publishCheckAdmission(ent)
}
