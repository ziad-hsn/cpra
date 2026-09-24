package persistence

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

var (
	ErrRecoveryIneligible   = errors.New("manual recovery is not eligible under current monitor state and policy")
	ErrRecoveryRateLimited  = errors.New("manual recovery request rate limit reached")
	ErrActionReviewConflict = errors.New("action observation or review changed")
	ErrExecutorUnfenced     = errors.New("the action executor is active or lacks verified fencing")
	ErrLocalExecutorActive  = errors.New("local action executors or unconfirmed completion markers remain; storage lock retained")
)

type ManualRecoveryCommand struct {
	Revision                string                       `json:"revision,omitempty"`
	MonitorUID              string                       `json:"monitor_uid"`
	ExpectedControlRevision string                       `json:"expected_control_revision"`
	OperationID             string                       `json:"operation_id"`
	Actor                   string                       `json:"actor"`
	Reason                  string                       `json:"reason"`
	Limits                  runtimeconfig.ManualRecovery `json:"limits"`
}

type ActionReviewCommand struct {
	ActionID         string   `json:"action_id"`
	MonitorUID       string   `json:"monitor_uid"`
	ExpectedRevision string   `json:"expected_revision"`
	Revision         string   `json:"revision"`
	OperationID      string   `json:"operation_id"`
	Actor            string   `json:"actor"`
	Reason           string   `json:"reason"`
	Note             string   `json:"note,omitempty"`
	Resolution       string   `json:"resolution"`
	EvidenceRefs     []string `json:"evidence_refs,omitempty"`
}

// ActionReview records an operator assertion separately from provider facts.
// Conflict preserves the assertion but restores the held disposition after
// contradictory provider evidence. Nothing here is a permission to replay work.
type ActionReview struct {
	Revision     string    `json:"revision"`
	Resolution   string    `json:"resolution"`
	Actor        string    `json:"actor"`
	At           time.Time `json:"at"`
	Reason       string    `json:"reason"`
	Note         string    `json:"note,omitempty"`
	EvidenceRefs []string  `json:"evidence_refs,omitempty"`
	Conflict     bool      `json:"conflict,omitempty"`
}

type ActionRecord struct {
	Action
	MonitorID      string
	ReviewRevision string
	Held           bool
	ExecutorFenced bool
}

// RequestRecovery copies the deployment's limits into the committed command.
// Callers select an exact monitor/control identity, never a rate-limit override.
func (s *Store) RequestRecovery(ctx context.Context, c Command) ([]Result, error) {
	if c.Kind != "manual_recovery" || c.ManualRecovery == nil {
		return nil, ErrControlInvalid
	}
	body := *c.ManualRecovery
	body.Limits = s.config.ManualRecovery.Effective()
	c.ManualRecovery = &body
	return s.Submit(ctx, []Command{c})
}

func validEvidenceRefs(refs []string) bool {
	if len(refs) > 8 {
		return false
	}
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		if ref == "" || len(ref) > 2048 || !utf8.ValidString(ref) || seen[ref] {
			return false
		}
		for _, r := range ref {
			if unicode.IsControl(r) {
				return false
			}
		}
		seen[ref] = true
	}
	return true
}
func validReviewResolution(s string) bool {
	return s == "accepted" || s == "rejected" || s == "inconclusive"
}
func validateRecoveryReviewCommand(c Command) error {
	if !catalogIdentifier(c.MonitorID, 256) {
		return ErrControlInvalid
	}
	extra := c
	extra.Kind, extra.MonitorID, extra.At = "", "", time.Time{}
	if c.Kind == "manual_recovery" {
		r := c.ManualRecovery
		if r == nil || !catalogIdentifier(r.MonitorUID, 256) || !catalogIdentifier(r.ExpectedControlRevision, 256) ||
			!catalogIdentifier(r.OperationID, 256) || (strings.HasPrefix(r.OperationID, "op.") && !validOperationIdentity(r.OperationID, r.Revision)) || (r.Revision != "" && !catalogIdentifier(r.Revision, 256)) || !catalogIdentifier(r.Actor, 128) || !controlText(r.Reason) || strings.TrimSpace(r.Reason) == "" || r.Limits.Validate() != nil ||
			c.Guard == nil || c.Guard.Removed || c.Guard.validate(c.MonitorID) != nil || c.Guard.monitorUID(c.MonitorID) != r.MonitorUID {
			return ErrControlInvalid
		}
		extra.ManualRecovery, extra.Guard = nil, nil
	} else {
		r := c.ActionReview
		if r == nil || !catalogIdentifier(r.MonitorUID, 256) || !catalogIdentifier(r.ActionID, 256) || !catalogIdentifier(r.ExpectedRevision, 256) ||
			!catalogIdentifier(r.Revision, 256) || !validOperationIdentity(r.OperationID, r.Revision) || r.Revision == r.ExpectedRevision || !catalogIdentifier(r.Actor, 128) ||
			!controlText(r.Reason) || strings.TrimSpace(r.Reason) == "" || !controlText(r.Note) || !validReviewResolution(r.Resolution) || !validEvidenceRefs(r.EvidenceRefs) {
			return ErrControlInvalid
		}
		extra.ActionReview = nil
	}
	if extra != (Command{}) {
		return ErrControlInvalid
	}
	return nil
}

func (f *machine) applyRecoveryReview(c Command, index uint64) Result {
	if c.Kind == "manual_recovery" {
		return f.applyManualRecovery(c, index)
	}
	return f.applyActionReview(c, index)
}
func (m Monitor) observedUnhealthy() bool {
	return !m.LastCheck.IsZero() && (m.LastOutcome == "failure" || m.LastOutcome == "timeout")
}
func (m Monitor) latestRecovery() (Action, bool) {
	var latest Action
	found := false
	for _, a := range m.Actions {
		if a.Kind != "intervention" || a.CatalogUID != m.CatalogUID || (a.IncidentID != "" && a.IncidentID != m.IncidentID) {
			continue
		}
		if !found || a.Attempt > latest.Attempt || (a.Attempt == latest.Attempt && (a.NotBefore.After(latest.NotBefore) || (a.NotBefore.Equal(latest.NotBefore) && a.ID > latest.ID))) {
			latest, found = a, true
		}
	}
	return latest, found
}
func (m Monitor) manualRecoveryEligible(at time.Time) bool {
	if m.Removed || !m.Policy.Enabled || m.snoozed(at) || m.Policy.InMaintenance(at) || !m.Policy.Intervention || !m.observedUnhealthy() ||
		m.pendingIntervention() || m.VerifyRemaining > 0 || m.recoveryAttempts() >= m.Policy.recoveryLimit() {
		return false
	}
	if m.recoveryAttempts() == 0 {
		return true
	}
	a, ok := m.latestRecovery()
	return ok && (a.State == Failed || (a.State == Unknown && !a.Held() && a.Review != nil && a.Review.Resolution == "rejected"))
}
func (f *machine) applyManualRecovery(c Command, index uint64) Result {
	r := c.ManualRecovery
	if err := f.checkCatalogGuard(c.MonitorID, c.Guard); err != nil {
		return Result{Err: err}
	}
	previous, ok := f.image.Monitors[c.MonitorID]
	current := f.image.Catalog[(CatalogKey{Kind: "Monitor", ID: c.MonitorID}).indexKey()]
	if !ok || previous.CatalogUID != r.MonitorUID || current.UID != r.MonitorUID || previous.ControlRevision != r.ExpectedControlRevision || previous.CatalogRevision != current.Revision || previous.DependencyRevision != c.Guard.revision() {
		return Result{Err: ErrControlConflict}
	}
	if !previous.manualRecoveryEligible(c.At) {
		return Result{Err: ErrRecoveryIneligible}
	}
	if _, exists := f.image.Operations[r.OperationID]; exists {
		return Result{Err: ErrControlConflict}
	}
	if f.pendingOperationCount() >= maxPendingCatalogOperations {
		return Result{Err: ErrCatalogBusy}
	}
	limits := r.Limits.Effective()
	kept := make([]time.Time, 0, min(100, len(previous.ManualRequests)+1))
	for _, at := range previous.ManualRequests {
		if c.At.Before(at.Add(limits.MinimumInterval)) {
			return Result{Err: ErrRecoveryRateLimited}
		}
		if at.After(c.At.Add(-time.Hour)) {
			kept = append(kept, at)
		}
	}
	if len(kept) >= limits.PerHour {
		return Result{Err: ErrRecoveryRateLimited}
	}
	m := previous.Clone()
	m.InterventionAttempts = m.recoveryAttempts() + 1
	if m.manualRecoveryDue(c.At).After(c.At) {
		return Result{Err: ErrRecoveryIneligible}
	}
	m.InterventionAttempted = true
	m.ManualRequests = append(kept, c.At)
	m.beginIncident(c.At)
	var actionID string
	var events []Event
	m.queueAction("intervention", "", 0, c.At, func(kind string, a *Action) {
		actionID = a.ID
		events = append(events, Event{MonitorID: m.ID, CatalogUID: a.CatalogUID, Revision: a.Revision, At: c.At, Type: kind, ActionID: a.ID, IncidentID: a.IncidentID, Kind: a.Kind, Actor: r.Actor, Reason: r.Reason})
	})
	if actionID == "" {
		return Result{Err: ErrRecoveryIneligible}
	}
	a := m.Actions[actionID]
	a.Manual, a.OperationID = true, r.OperationID
	m.Actions[actionID] = a
	events = append(events, f.installMonitor(previous, m, c.At)...)
	receipt := OperationReceipt{Subject: "recovery", ActionID: actionID, ID: r.OperationID, Key: current.Key, UID: m.CatalogUID, OldVersion: r.ExpectedControlRevision, NewVersion: manualRecoveryRevision(r), Generation: current.Generation, CommittedIndex: index, Actor: r.Actor, At: c.At, UpdatedAt: c.At, State: "committed", Outcome: "committed"}
	f.addActionReceipt(receipt)
	events = append(events, receiptEvent(receipt))
	return Result{Allowed: true, Monitor: &m, Operation: &receipt, Events: events}
}
func (f *machine) addActionReceipt(r OperationReceipt) {
	if f.image.Operations == nil {
		f.image.Operations = make(map[string]OperationReceipt)
	}
	f.putOperation(r)
	f.image.Version = max(f.image.Version, CatalogFormatVersion)
}
func (f *machine) applyActionReview(c Command, index uint64) Result {
	r := c.ActionReview
	previous, ok := f.image.Monitors[c.MonitorID]
	a, exists := previous.Actions[r.ActionID]
	if !ok || !exists || a.CatalogUID != r.MonitorUID || a.State != Unknown || ActionReviewRevision(a) != r.ExpectedRevision {
		return Result{Err: ErrActionReviewConflict}
	}
	if r.Resolution != "inconclusive" {
		if !actionExecutorFenced(a, f.image.LocalExecutorSession) {
			return Result{Err: ErrExecutorUnfenced}
		}
		if a.ConflictingEvidence != nil || (a.LateEvidence != nil && a.LateEvidence.Outcome != r.Resolution) {
			return Result{Err: ErrLateEvidenceConflict}
		}
	}
	if (a.Review != nil && c.At.Before(a.Review.At)) || (!a.FinishedAt.IsZero() && c.At.Before(a.FinishedAt)) {
		return Result{Err: ErrActionReviewConflict}
	}
	if _, exists := f.image.Operations[r.OperationID]; exists {
		return Result{Err: ErrActionReviewConflict}
	}
	if f.pendingOperationCount() >= maxPendingCatalogOperations {
		return Result{Err: ErrCatalogBusy}
	}
	m := previous.Clone()
	a = a.Clone()
	a.Review = &ActionReview{Revision: r.Revision, Resolution: r.Resolution, Actor: r.Actor, At: c.At, Reason: r.Reason, Note: r.Note, EvidenceRefs: append([]string(nil), r.EvidenceRefs...)}
	m.Actions[a.ID] = a
	events := f.installMonitor(previous, m, c.At)
	events = append(events, Event{MonitorID: m.ID, CatalogUID: a.CatalogUID, Revision: a.Revision, At: c.At, Type: "action_reviewed", ActionID: a.ID, IncidentID: a.IncidentID, Kind: a.Kind, Color: a.Color, Endpoint: a.Endpoint, Actor: r.Actor, Reason: r.Reason, Note: r.Note, EvidenceRefs: append([]string(nil), r.EvidenceRefs...), Outcome: r.Resolution})
	// Historical actions retain their original UID even after public ID reuse.
	generation := f.image.Catalog[(CatalogKey{Kind: "Monitor", ID: m.ID}).indexKey()].Generation
	receipt := OperationReceipt{Subject: "review", ActionID: a.ID, ID: r.OperationID, Key: CatalogKey{Kind: "Monitor", ID: m.ID}, UID: a.CatalogUID, OldVersion: r.ExpectedRevision, NewVersion: r.Revision, Generation: max(1, generation), CommittedIndex: index, Actor: r.Actor, At: c.At, UpdatedAt: c.At, State: "committed", Outcome: "committed"}
	f.addActionReceipt(receipt)
	events = append(events, receiptEvent(receipt))
	return Result{Allowed: true, Monitor: &m, Operation: &receipt, Events: events}
}

// Only direct action receipt IDs are visited. This does not scan the global
// operation ledger on every health result.
func (f *machine) supersedeActionReceipts(previous Monitor, at time.Time) []Event {
	var events []Event
	for _, actionID := range sortedActions(previous.Actions) {
		a := previous.Actions[actionID]
		ids := []string{a.OperationID}
		if a.Review != nil {
			if receipt, ok := f.operationByVersion(CatalogKey{Kind: "Monitor", ID: previous.ID}, a.CatalogUID, "review", a.Review.Revision); ok {
				ids = append(ids, receipt.ID)
			}
		}
		for _, id := range ids {
			r, ok := f.image.Operations[id]
			if !ok || (r.Subject != "review" && r.Subject != "recovery") || operationCurrent(f.image, r) {
				continue
			}
			r.State, r.Outcome = "partial", "superseded"
			r.UpdatedAt = at
			if at.Before(r.At) {
				r.UpdatedAt = r.At
			}
			f.deleteOperation(id)
			events = append(events, receiptEvent(r))
		}
	}
	return events
}

func (m Monitor) otherActiveRecovery(id string) bool {
	for _, a := range m.Actions {
		if a.ID != id && a.CatalogUID == m.CatalogUID && a.Kind == "intervention" && (a.State == Started || a.Held()) {
			return true
		}
	}
	return false
}

// An operator rejection is not a provider failure. Its explicit manual retry
// path nevertheless retains the configured cooldown using conservative audit
// times; no automatic retry permission or failure counter is manufactured.
func (m Monitor) manualRecoveryDue(at time.Time) time.Time {
	due := m.recoveryDue(at)
	for _, a := range m.Actions {
		if a.Kind != "intervention" || a.CatalogUID != m.CatalogUID || a.IncidentID != m.IncidentID || a.State != Unknown || a.Held() || a.Review == nil || a.Review.Resolution != "rejected" {
			continue
		}
		last := a.Review.At
		if a.FinishedAt.After(last) {
			last = a.FinishedAt
		}
		if a.ExecutorFinishedAt.After(last) {
			last = a.ExecutorFinishedAt
		}
		if next := last.Add(m.Policy.RecoveryCooldown); next.After(due) {
			due = next
		}
	}
	return due
}

func manualRecoveryRevision(r *ManualRecoveryCommand) string {
	if r.Revision != "" {
		return r.Revision
	}
	return r.OperationID
}
