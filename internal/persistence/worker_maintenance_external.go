//go:build externaljobs

package persistence

import (
	"context"
	"time"

	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

// WorkerMaintenanceDecision is server-prepared command data, never worker input.
// Its configuration pin and evaluation time are checked again during Apply.
// Until is the next possible maintenance start, fencing delayed grant disclosure.
type WorkerMaintenanceDecision struct {
	ExecutionID        string    `json:"execution_id"`
	ExecutionRevision  string    `json:"execution_revision"`
	MonitorUID         string    `json:"monitor_uid"`
	MonitorRevision    string    `json:"monitor_revision"`
	DependencyRevision string    `json:"dependency_revision"`
	At                 time.Time `json:"at"`
	Until              time.Time `json:"until,omitzero"`
	Available          bool      `json:"available"`
	Active             bool      `json:"active"`
	SinceQueued        bool      `json:"since_queued"`
}

// WorkerMaintenanceEvaluator opens only the pinned Monitor configuration. Store
// invokes it on detached metadata, outside all persistence/controller locks.
type WorkerMaintenanceEvaluator func(context.Context, WorkerExecutionIntent, time.Time, time.Time) (WorkerMaintenanceDecision, error)

func workerMaintenanceIdentity(in WorkerExecutionIntent, at time.Time) WorkerMaintenanceDecision {
	return WorkerMaintenanceDecision{ExecutionID: in.ID, ExecutionRevision: in.Revision,
		MonitorUID: in.MonitorUID, MonitorRevision: in.MonitorRevision, DependencyRevision: in.Guard.revision(), At: at}
}

func (d WorkerMaintenanceDecision) validate(at time.Time) error {
	for _, id := range []string{d.ExecutionID, d.ExecutionRevision, d.MonitorUID, d.MonitorRevision, d.DependencyRevision} {
		if !catalogIdentifier(id, 256) {
			return ErrWorkerExecutionInvalid
		}
	}
	if !d.At.Equal(at) || !d.Until.IsZero() && (!d.Until.After(at) || d.Until.Year() > 9999) {
		return ErrWorkerExecutionInvalid
	}
	return nil
}

func (d *WorkerMaintenanceDecision) matches(in WorkerExecutionIntent, at time.Time) bool {
	return d != nil && d.Available && d.validate(at) == nil && d.ExecutionID == in.ID &&
		d.ExecutionRevision == in.Revision && d.MonitorUID == in.MonitorUID && d.MonitorRevision == in.MonitorRevision &&
		d.DependencyRevision == in.Guard.revision()
}

func (f *machine) workerMaintenanceAllowed(d *WorkerMaintenanceDecision, in WorkerExecutionIntent, at time.Time) bool {
	if !d.matches(in, at) {
		return false
	}
	m := f.image.Monitors[in.MonitorID]
	return in.Category == "check" || m.ControlNotification(m.Actions[in.ActionID]) ||
		!d.Active && (in.Category != "notification" || !d.SinceQueued)
}

func (f *machine) workerMaintenanceCancellation(d *WorkerMaintenanceDecision, in WorkerExecutionIntent, at time.Time, version int) (*Monitor, []Event) {
	if !lifecycleStorageFormat(version) || in.Category != "notification" || !d.matches(in, at) ||
		!d.Active && !d.SinceQueued || f.checkCatalogGuard(in.MonitorID, &in.Guard) != nil {
		return nil, nil
	}
	previous := f.image.Monitors[in.MonitorID]
	a := previous.Actions[in.ActionID]
	if a.State != Queued || previous.ControlNotification(a) {
		return nil, nil
	}
	r := transitionAtFormat(previous, Command{Kind: "start", MonitorID: in.MonitorID, Revision: in.MonitorRevision,
		ActionID: in.ActionID, At: at, Maintenance: true, NoticeRevision: ActionNoticeRevision(a)}, version)
	return r.Monitor, r.Events
}

type workerMaintenanceInput struct {
	intent WorkerExecutionIntent
	policy Policy
	queued time.Time
}

// A new cursor is installed for every preparation. Its identity prevents an
// older evaluator from publishing progress after a newer preparation or eviction.
type workerMaintenanceCursor struct {
	position int
}

func (f *machine) workerMaintenanceInput(id string) (workerMaintenanceInput, bool) {
	if f.image.WorkerExecutions == nil {
		return workerMaintenanceInput{}, false
	}
	r, ok := f.image.WorkerExecutions.Records[id]
	if !ok {
		return workerMaintenanceInput{}, false
	}
	m := f.image.Monitors[r.Intent.MonitorID]
	in := r.Intent
	in.Guard = in.Guard.Clone()
	in.Schedule = nil
	in.Payload = secureconfig.Envelope{}
	queued := m.Actions[in.ActionID].QueuedAt
	if in.Category == "check" && r.Lifecycle != nil {
		queued = r.Lifecycle.StartedAt
	}
	return workerMaintenanceInput{intent: in, policy: m.Policy.Clone(), queued: queued}, true
}

func prepareWorkerMaintenance(ctx context.Context, in workerMaintenanceInput, at time.Time, evaluators []WorkerMaintenanceEvaluator) (*WorkerMaintenanceDecision, error) {
	d := workerMaintenanceIdentity(in.intent, at)
	if len(evaluators) > 1 {
		return nil, ErrWorkerExecutionInvalid
	}
	if len(evaluators) == 1 && evaluators[0] != nil {
		v, err := evaluators[0](ctx, in.intent, in.queued, at)
		if err != nil {
			return nil, err
		}
		// The evaluator owns only the decision, not the identity being authorized.
		d.Available, d.Active, d.SinceQueued, d.Until = v.Available, v.Active, v.SinceQueued, v.Until
	} else if !in.policy.PeriodicMaintenance {
		d.Available = true
		d.Active = in.policy.InMaintenance(at)
		d.SinceQueued = in.policy.MaintenanceBetween(in.queued, at)
		for _, w := range in.policy.Maintenance {
			if w.Start.After(at) && (d.Until.IsZero() || w.Start.Before(d.Until)) {
				d.Until = w.Start
			}
		}
	}
	return &d, d.validate(at)
}

func (s *Store) prepareWorkerMaintenance(ctx context.Context, id, purpose string, at time.Time, evaluators []WorkerMaintenanceEvaluator) (*WorkerMaintenanceDecision, error) {
	unlock, err := s.lockControllerState(ctx, false)
	if err != nil {
		return nil, err
	}
	in, ok := s.fsm.workerMaintenanceInput(id)
	needed := ok
	if ok {
		r := s.fsm.image.WorkerExecutions.Records[id]
		_, terminal := r.TerminalReceipt()
		needed = !terminal && (purpose != "start" || r.Lifecycle != nil && r.Lifecycle.Phase == "offered")
	}
	unlock()
	if !needed {
		return nil, nil // Original receipt/expiry handling remains authoritative.
	}
	d, err := prepareWorkerMaintenance(ctx, in, at, evaluators)
	if err != nil && purpose == "result" && ctx.Err() == nil {
		// Retain known outcomes even when follow-up policy cannot be prepared.
		// An unavailable decision suppresses new work; it never changes facts.
		unavailable := workerMaintenanceIdentity(in.intent, at)
		return &unavailable, nil
	}
	return d, err
}

func (s *Store) prepareWorkerPollMaintenance(ctx context.Context, c WorkerSessionCommand, at time.Time, evaluators []WorkerMaintenanceEvaluator) ([]WorkerMaintenanceDecision, error) {
	unlock, err := s.lockControllerState(ctx, true)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		input   workerMaintenanceInput
		wire    int
		control bool
	}
	var inputs []candidate
	var cursor *workerMaintenanceCursor
	var next int
	if s.fsm.image.WorkerExecutions != nil && s.fsm.image.JobTypes != nil {
		// Restored snapshots contain records, not the derived ready index. Build
		// it before choosing which intents need a committed maintenance decision.
		if err := s.fsm.image.WorkerExecutions.ensureIndexes(ctx); err != nil {
			unlock()
			return nil, err
		}
		ordered := s.fsm.workerOfferOrder(c)
		s.workerMaintenanceMu.Lock()
		if s.workerMaintenanceScan == nil {
			s.workerMaintenanceScan = make(map[string]*workerMaintenanceCursor)
		}
		cursor = &workerMaintenanceCursor{}
		if previous, exists := s.workerMaintenanceScan[c.Authority.WorkerUID]; exists {
			cursor.position = previous.position
		} else {
			if len(s.workerMaintenanceScan) >= MaxWorkerSessions {
				for key := range s.workerMaintenanceScan {
					delete(s.workerMaintenanceScan, key)
					break
				}
			}
		}
		s.workerMaintenanceScan[c.Authority.WorkerUID] = cursor
		if len(ordered) > 0 {
			start := cursor.position % len(ordered)
			visited := 0
			for visited < len(ordered) && len(inputs) < 100 {
				item := ordered[(start+visited)%len(ordered)]
				visited++
				record := s.fsm.image.WorkerExecutions.Records[item.id]
				if !s.fsm.workerAssignmentEligible(record, c.Authority, c.OwnerEpoch, at) {
					continue
				}
				if in, ok := s.fsm.workerMaintenanceInput(item.id); ok {
					monitor := s.fsm.image.Monitors[in.intent.MonitorID]
					inputs = append(inputs, candidate{input: in, wire: len(record.Intent.Payload.Ciphertext) + workerOfferWireOverhead,
						control: monitor.ControlNotification(monitor.Actions[in.intent.ActionID])})
				}
			}
			next = (start + visited) % len(ordered)
		}
		s.workerMaintenanceMu.Unlock()
	}
	if s.fsm.err != nil {
		unlock()
		return nil, errControllerUnavailable
	}
	unlock()
	out := make([]WorkerMaintenanceDecision, 0, len(inputs))
	available, wire := 0, 0
	for _, item := range inputs {
		in := item.input
		d, err := prepareWorkerMaintenance(ctx, in, at, evaluators)
		if err != nil {
			return nil, err
		}
		allowed := d.Available && (in.intent.Category == "check" || item.control ||
			!d.Active && (in.intent.Category != "notification" || !d.SinceQueued))
		if allowed && item.wire > workerOfferWireBudget-wire {
			// Apply restores committed ordering. Every allowed decision emitted
			// here must fit together, even when this scan began mid-page.
			continue
		}
		out = append(out, *d) // Suppressed decisions still commit cancellations.
		if allowed {
			wire += item.wire
			available++
		}
		if available >= min(c.Request.Capacity, c.Request.Limit) {
			// Stop opening encrypted configurations once this response can fill.
			// Restart at the oldest remaining entry: committed offers will leave
			// the ready index, so carrying its old numeric offset skips work.
			next = 0
			break
		}
	}
	// A full suppressed page must still advance, allowing later eligible work
	// to progress. A concurrent preparation's newer cursor is only a scan hint;
	// do not overwrite it or hold this mutex while evaluating configuration.
	s.workerMaintenanceMu.Lock()
	if cursor != nil && s.workerMaintenanceScan[c.Authority.WorkerUID] == cursor {
		cursor.position = next
	}
	s.workerMaintenanceMu.Unlock()
	return out, nil
}
