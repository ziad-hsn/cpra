//go:build externaljobs

package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

const WorkerFinalizationFormatVersion = 24
const workerFinalizationSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-24\n"
const MaxWorkerFinalizationsPerTick = 128
const maxWorkerFinalizationBytes = 8 << 10

// WorkerFinalization records the server's missing-result decision. It contains
// no invented worker report, provider effect, or handler completion evidence.
type WorkerFinalization struct {
	Receipt        WorkerReceipt `json:"receipt"`
	Reason         string        `json:"reason"`
	OwnerEpoch     string        `json:"owner_epoch"`
	RecordedAt     time.Time     `json:"recorded_at"`
	CommittedIndex uint64        `json:"committed_index"`
	NoData         bool          `json:"no_data,omitempty"`
	TimedOut       bool          `json:"timed_out,omitempty"`
}

type WorkerFinalizeCommand struct {
	ExecutionID       string `json:"execution_id"`
	ExecutionRevision string `json:"execution_revision"`
	OwnerEpoch        string `json:"owner_epoch"`
}

func (c WorkerFinalizeCommand) validate(at time.Time) error {
	if !catalogIdentifier(c.ExecutionID, 256) || !catalogIdentifier(c.ExecutionRevision, 256) ||
		!catalogIdentifier(c.OwnerEpoch, 256) || at.IsZero() || at.Year() < 1 || at.Year() > 9998 {
		return ErrWorkerExecutionInvalid
	}
	return nil
}

// TerminalReceipt returns the original authoritative result or finalization.
// A finalization is never replaced by a subsequently received worker result.
func (r WorkerExecutionRecord) TerminalReceipt() (WorkerReceipt, bool) {
	if r.Result != nil {
		return r.Result.Receipt, true
	}
	if r.Finalization != nil {
		return r.Finalization.Receipt, true
	}
	return WorkerReceipt{}, false
}

func (r WorkerExecutionRecord) terminalPosition() (time.Time, uint64) {
	if r.Result != nil {
		return r.Result.RecordedAt, r.Result.CommittedIndex
	}
	if r.Finalization != nil {
		return r.Finalization.RecordedAt, r.Finalization.CommittedIndex
	}
	return time.Time{}, 0
}

func workerFinalizationReason(r WorkerExecutionRecord, owner string, at time.Time) string {
	if r.Result != nil || at.Before(r.CreatedAt) || r.Lifecycle != nil && at.Before(r.Lifecycle.UpdatedAt) {
		return ""
	}
	if !at.Before(r.Intent.Deadline) {
		return "deadline"
	}
	if r.OwnerEpoch != owner {
		return "owner_lost"
	}
	if r.Lifecycle != nil && r.Lifecycle.GrantID == "" && len(r.RetiredOffers) == MaxWorkerRetiredOffers && !at.Before(r.Lifecycle.OfferDeadline) {
		return "offer_limit"
	}
	if r.Lifecycle != nil && r.Lifecycle.Phase == "rejected" {
		return "not_started"
	}
	return ""
}

func workerFinalizationReceipt(r WorkerExecutionRecord, index uint64) WorkerReceipt {
	receipt := WorkerReceipt{ExecutionID: r.Intent.ID, Disposition: "finalized",
		ReceiptID: identity(fmt.Sprintf("cpra/worker/finalization/v1\x00%s\x00%s\x00%d", r.Intent.ID, r.Intent.Revision, index))}
	if l := r.Lifecycle; l != nil {
		receipt.ServerID, receipt.WorkerUID = l.ServerID, l.WorkerUID
		if r.Intent.Category != "check" && l.GrantID != "" {
			receipt.Disposition = "unknown"
		}
	}
	return receipt
}

func workerFinalizationCost(v *WorkerFinalization) (int64, error) {
	if v == nil {
		return 0, nil
	}
	raw, err := json.Marshal(v)
	if err != nil || len(raw) > maxWorkerFinalizationBytes {
		return 0, ErrWorkerExecutionQuota
	}
	return int64(len(raw)), nil
}

func (f *machine) applyWorkerFinalization(c WorkerFinalizeCommand, at time.Time, index uint64) Result {
	if c.OwnerEpoch != f.image.LocalExecutorSession {
		return Result{Err: ErrWorkerSessionExpired}
	}
	state := f.image.WorkerExecutions
	if state == nil {
		return Result{Err: ErrWorkerExecutionConflict}
	}
	old, ok := state.Records[c.ExecutionID]
	if !ok || old.Intent.Revision != c.ExecutionRevision {
		return Result{Err: ErrWorkerExecutionConflict}
	}
	if _, terminal := old.TerminalReceipt(); terminal {
		return Result{Allowed: true}
	}
	reason := workerFinalizationReason(old, c.OwnerEpoch, at)
	if reason == "" {
		return Result{Err: ErrWorkerExecutionConflict}
	}
	r := old.Clone()
	r.Finalization = &WorkerFinalization{Receipt: workerFinalizationReceipt(r, index), Reason: reason,
		OwnerEpoch: c.OwnerEpoch, RecordedAt: at, CommittedIndex: index, NoData: r.Intent.Category == "check",
		TimedOut: r.Intent.Category == "check" && reason == "deadline"}
	if l := r.Lifecycle; l != nil {
		l.UpdatedAt = at
		switch {
		case l.GrantID == "":
			l.Phase = "rejected"
		case r.Intent.Category == "check":
			l.Phase = "finished"
		default:
			l.Phase = "unknown"
		}
	}
	if _, err := workerFinalizationCost(r.Finalization); err != nil {
		return Result{Err: err}
	}
	oldCost, err := workerExecutionRecordCost(r.Intent.ID, old)
	if err != nil {
		return Result{Err: err}
	}
	cost, err := workerExecutionRecordCost(r.Intent.ID, r)
	if err != nil || cost > oldCost {
		return Result{Err: ErrWorkerExecutionQuota}
	}
	monitor, events := f.workerFinalizationTransition(r, at)
	f.finishWorkerSLO(r, at)
	state.replaceRecord(r)
	state.EncodedBytes += cost - oldCost
	f.image.Version = max(f.image.Version, WorkerFinalizationFormatVersion)
	if monitor != nil {
		previous := f.image.Monitors[monitor.ID]
		events = append(events, f.installMonitor(previous, *monitor, at)...)
		f.publishWorkerMonitor(previous, *monitor)
	}
	return Result{Allowed: true, Monitor: monitor, Events: events}
}

func (f *machine) workerFinalizationTransition(r WorkerExecutionRecord, at time.Time) (*Monitor, []Event) {
	in := r.Intent
	previous, ok := f.image.Monitors[in.MonitorID]
	if !ok {
		return nil, nil
	}
	if in.Category == "check" {
		if previous.Removed || previous.CatalogUID != in.MonitorUID || previous.Revision != in.MonitorRevision || in.Generation <= previous.Generation {
			return nil, nil
		}
		m := previous.Clone()
		m.Generation = in.Generation
		if next := in.Scheduled.Add(m.Policy.Interval); next.After(m.NextCheck) {
			m.NextCheck = next
		}
		return &m, nil
	}
	a, ok := previous.Actions[in.ActionID]
	if !ok || a.CatalogUID != in.MonitorUID || a.Revision != in.MonitorRevision {
		return nil, nil
	}
	typeName := "action_cancelled"
	if r.Lifecycle != nil && r.Lifecycle.GrantID != "" {
		if a.State != Started || a.ExecutorKind != "worker" || a.ExecutorSession != r.Lifecycle.GrantID {
			return nil, nil
		}
		a.State, a.Outcome = Unknown, "worker_outcome_unknown"
		typeName = "action_unknown"
	} else {
		if a.State != Queued {
			return nil, nil
		}
		a.State, a.Outcome = Cancelled, "worker_not_started"
	}
	a.FinishedAt = at
	m := previous.Clone()
	m.Actions[a.ID] = a
	event := Event{MonitorID: m.ID, CatalogUID: a.CatalogUID, Revision: a.Revision, At: at,
		Type: typeName, IncidentID: a.IncidentID, ActionID: a.ID, Kind: a.Kind, Color: a.Color, Endpoint: a.Endpoint, Outcome: a.Outcome}
	return &m, []Event{event}
}

func validateWorkerFinalization(i image, r WorkerExecutionRecord) error {
	v := r.Finalization
	if v == nil {
		return nil
	}
	if i.Version < WorkerFinalizationFormatVersion || r.Result != nil || !catalogIdentifier(v.OwnerEpoch, 256) ||
		v.RecordedAt.Before(r.CreatedAt) || v.RecordedAt.Year() > 9998 || v.CommittedIndex < r.CommittedIndex || v.CommittedIndex > i.Index ||
		v.Receipt != workerFinalizationReceipt(r, v.CommittedIndex) || v.NoData != (r.Intent.Category == "check") ||
		v.TimedOut != (v.NoData && v.Reason == "deadline") {
		return ErrWorkerExecutionInvalid
	}
	if l := r.Lifecycle; l != nil {
		if !l.UpdatedAt.Equal(v.RecordedAt) || l.GrantID == "" && l.Phase != "rejected" ||
			l.GrantID != "" && ((v.NoData && l.Phase != "finished") || (!v.NoData && l.Phase != "unknown")) {
			return ErrWorkerExecutionInvalid
		}
	}
	switch v.Reason {
	case "deadline":
		if v.RecordedAt.Before(r.Intent.Deadline) {
			return ErrWorkerExecutionInvalid
		}
	case "owner_lost":
		if v.OwnerEpoch == r.OwnerEpoch || !v.RecordedAt.Before(r.Intent.Deadline) {
			return ErrWorkerExecutionInvalid
		}
	case "offer_limit":
		if i.Version < WorkerSchedulingFormatVersion || v.OwnerEpoch != r.OwnerEpoch || r.Lifecycle == nil || r.Lifecycle.GrantID != "" || len(r.RetiredOffers) != MaxWorkerRetiredOffers || v.RecordedAt.Before(r.Lifecycle.OfferDeadline) || !v.RecordedAt.Before(r.Intent.Deadline) {
			return ErrWorkerExecutionInvalid
		}
	case "not_started":
		if v.OwnerEpoch != r.OwnerEpoch || r.Lifecycle == nil || r.Lifecycle.GrantID != "" || !v.RecordedAt.Before(r.Intent.Deadline) {
			return ErrWorkerExecutionInvalid
		}
	default:
		return ErrWorkerExecutionInvalid
	}
	_, err := workerFinalizationCost(v)
	return err
}

// FinalizeWorkerExecutions commits at most limit due records. Time is captured
// outside Apply; each command rechecks the owner and original execution revision.
func (s *Store) FinalizeWorkerExecutions(ctx context.Context, at time.Time, limit int) (int, error) {
	if ctx == nil || at.IsZero() || at.Year() < 1 || at.Year() > 9998 || limit < 1 || limit > MaxWorkerFinalizationsPerTick {
		return 0, ErrWorkerExecutionInvalid
	}
	if s.administrative {
		return 0, ErrWorkerSessionUnavailable
	}
	unlock, err := s.lockControllerState(ctx, false)
	if err != nil {
		return 0, err
	}
	state := s.fsm.image.WorkerExecutions
	var ids []string
	if state != nil {
		if len(state.Records) > MaxWorkerExecutions {
			unlock()
			return 0, ErrWorkerExecutionQuota
		}
		for id, record := range state.Records {
			if err := ctx.Err(); err != nil {
				unlock()
				return 0, err
			}
			if _, terminal := record.TerminalReceipt(); !terminal && workerFinalizationReason(record, s.executorSession, at) != "" {
				ids = append(ids, id)
			}
		}
	}
	slices.Sort(ids)
	commands := make([]Command, 0, min(len(ids), limit, s.config.Storage.BatchSize))
	for _, id := range ids[:min(len(ids), limit, s.config.Storage.BatchSize)] {
		r := state.Records[id]
		c := WorkerFinalizeCommand{ExecutionID: id, ExecutionRevision: r.Intent.Revision, OwnerEpoch: s.executorSession}
		commands = append(commands, Command{Kind: "worker_finalize", At: at, commandExtensions: commandExtensions{WorkerFinalize: &c}})
	}
	unlock()
	if len(commands) == 0 {
		return 0, ctx.Err()
	}
	results, err := s.Submit(ctx, commands)
	if err != nil {
		return 0, errors.Join(ErrCommitUnconfirmed, err)
	}
	if len(results) != len(commands) {
		return 0, errors.Join(ErrCommitUnconfirmed, ErrWorkerExecutionUnavailable)
	}
	count := 0
	for _, result := range results {
		if result.Err != nil {
			return count, result.Err
		}
		if !result.Allowed {
			return count, ErrWorkerExecutionUnavailable
		}
		count++
	}
	return count, nil
}

func (s *Store) maintainExtensions(at time.Time) error {
	if !s.config.ExternalJobs.Enabled {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.ControllerHealthContext(ctx); err != nil {
		return nil // Opening, restore and leadership recovery are not write failures.
	}
	_, err := s.RequeueWorkerExecutions(ctx, at, MaxWorkerRequeuesPerTick)
	if err == nil {
		_, err = s.FinalizeWorkerExecutions(ctx, at, MaxWorkerFinalizationsPerTick)
	}
	if err == nil {
		_, err = s.RetireWorkerChecks(ctx, at, MaxWorkerCheckRetirements)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || IsLeadershipUnavailable(err) ||
		errors.Is(err, ErrWorkerSessionExpired) || errors.Is(err, ErrWorkerExecutionConflict) {
		return nil // A later tick observes the current committed owner/state.
	}
	select {
	case <-s.stop:
		return nil
	default:
	}
	return err
}
