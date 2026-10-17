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
		ActionID: in.ActionID, At: at, Maintenance: true}, version)
	return r.Monitor, r.Events
}

type workerMaintenanceInput struct {
	intent WorkerExecutionIntent
	policy Policy
	queued time.Time
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
	return workerMaintenanceInput{intent: in, policy: m.Policy.Clone(), queued: m.Actions[in.ActionID].QueuedAt}, true
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
	unlock, err := s.lockControllerState(ctx, false)
	if err != nil {
		return nil, err
	}
	var inputs []workerMaintenanceInput
	if s.fsm.image.WorkerExecutions != nil && s.fsm.image.JobTypes != nil {
		ordered := s.fsm.workerOfferOrder(c)
		s.workerMaintenanceMu.Lock()
		if s.workerMaintenanceScan == nil {
			s.workerMaintenanceScan = make(map[string]int)
		}
		if _, exists := s.workerMaintenanceScan[c.Authority.WorkerUID]; !exists && len(s.workerMaintenanceScan) >= MaxWorkerSessions {
			for key := range s.workerMaintenanceScan {
				delete(s.workerMaintenanceScan, key)
				break
			}
		}
		if len(ordered) > 0 {
			start := s.workerMaintenanceScan[c.Authority.WorkerUID] % len(ordered)
			visited := 0
			for visited < len(ordered) && len(inputs) < 100 {
				candidate := ordered[(start+visited)%len(ordered)]
				visited++
				if !s.fsm.workerAssignmentEligible(s.fsm.image.WorkerExecutions.Records[candidate.id], c.Authority, c.OwnerEpoch, at) {
					continue
				}
				if in, ok := s.fsm.workerMaintenanceInput(candidate.id); ok {
					inputs = append(inputs, in)
				}
			}
			s.workerMaintenanceScan[c.Authority.WorkerUID] = (start + visited) % len(ordered)
		}
		s.workerMaintenanceMu.Unlock()
	}
	unlock()
	out := make([]WorkerMaintenanceDecision, 0, len(inputs))
	for _, in := range inputs {
		d, err := prepareWorkerMaintenance(ctx, in, at, evaluators)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, nil
}
