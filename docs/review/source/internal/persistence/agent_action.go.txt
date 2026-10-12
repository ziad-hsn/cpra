package persistence

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const AgentActionFormatVersion = 31
const agentActionSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-31\n"

var (
	ErrAgentActionInvalid   = errors.New("invalid agent action")
	ErrAgentReceiptInvalid  = errors.New("invalid agent receipt")
	ErrAgentReceiptConflict = errors.New("agent receipt conflicts with the original action")
)

// AgentReceipt retains correlation and protocol state only. Agent content,
// artifacts, target addresses and credentials never enter this record.
type AgentReceipt struct {
	Sequence   uint64    `json:"sequence"`
	TaskID     string    `json:"task_id,omitempty"`
	ContextID  string    `json:"context_id,omitempty"`
	MessageID  string    `json:"message_id,omitempty"`
	State      string    `json:"state,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
}

type AgentReceiptCommand struct {
	ExpectedSequence uint64       `json:"expected_sequence"`
	Receipt          AgentReceipt `json:"receipt"`
}

func agentActionStorageFormat(version int) bool {
	return version == AgentActionFormatVersion || recoveryNotificationStorageFormat(version) || version > AgentActionFormatVersion && externalStorageFormat(version)
}

func agentIdentifier(value string, limit int) bool {
	return value != "" && len(value) <= limit && utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) == -1
}

func (r AgentReceipt) validate() error {
	if r.Sequence == 0 || r.ObservedAt.IsZero() || r.ContextID != "" && !agentIdentifier(r.ContextID, 256) {
		return ErrAgentReceiptInvalid
	}
	if r.TaskID != "" {
		if !agentIdentifier(r.TaskID, 256) || strings.ContainsAny(r.TaskID, "/\\%:") || r.TaskID == "." || r.TaskID == ".." ||
			r.MessageID != "" || !agentIdentifier(r.State, 128) {
			return ErrAgentReceiptInvalid
		}
	} else if !agentIdentifier(r.MessageID, 256) || r.State != "" {
		return ErrAgentReceiptInvalid
	}
	return nil
}

func validateAgentReceiptCommand(c Command) error {
	if c.AgentReceipt == nil || !catalogIdentifier(c.MonitorID, 256) || !catalogIdentifier(c.ActionID, 256) ||
		!catalogIdentifier(c.Revision, 256) || !catalogIdentifier(c.ExecutorSession, 256) {
		return ErrAgentReceiptInvalid
	}
	r := c.AgentReceipt
	if r.ExpectedSequence == math.MaxUint64 || r.Receipt.Sequence != r.ExpectedSequence+1 ||
		!c.At.Equal(r.Receipt.ObservedAt) || r.Receipt.validate() != nil {
		return ErrAgentReceiptInvalid
	}
	extra := c
	extra.Kind, extra.MonitorID, extra.ActionID, extra.Revision, extra.ExecutorSession = "", "", "", "", ""
	extra.At, extra.AgentReceipt = time.Time{}, nil
	if extra != (Command{}) {
		return ErrAgentReceiptInvalid
	}
	return nil
}

func validateAgentStart(c Command) error {
	if !catalogIdentifier(c.MonitorID, 256) || !catalogIdentifier(c.ActionID, 256) ||
		!catalogIdentifier(c.Revision, 256) || !catalogIdentifier(c.ExecutorSession, 256) {
		return ErrAgentActionInvalid
	}
	if c.Guard != nil && (c.Guard.Removed || c.Guard.validate(c.MonitorID) != nil) {
		return ErrAgentActionInvalid
	}
	if c.AgentConnection != nil && validateAgentConnection(c.AgentConnection) != nil {
		return ErrAgentActionInvalid
	}
	extra := c
	extra.Kind, extra.MonitorID, extra.ActionID, extra.Revision, extra.ExecutorSession, extra.Driver = "", "", "", "", "", ""
	extra.At, extra.Guard, extra.Maintenance = time.Time{}, nil, false
	extra.ActionDeadline = time.Time{}
	extra.AgentConnection = nil
	if extra != (Command{}) {
		return ErrAgentActionInvalid
	}
	return nil
}

func agentTerminal(state string) bool {
	switch state {
	case "TASK_STATE_COMPLETED", "TASK_STATE_FAILED", "TASK_STATE_CANCELED", "TASK_STATE_REJECTED":
		return true
	default:
		return false
	}
}

func agentSuccess(a Action) bool {
	r := a.AgentReceipt
	if r == nil {
		return false
	}
	if a.Kind == "intervention" {
		return r.TaskID != "" && r.State == "TASK_STATE_COMPLETED"
	}
	// Notification success establishes acceptance of investigation context.
	// It does not establish completed agent work or monitor recovery.
	return a.Kind == "code" && (r.MessageID != "" || r.State == "TASK_STATE_SUBMITTED" || r.State == "TASK_STATE_WORKING" || r.State == "TASK_STATE_COMPLETED")
}

func validateAgentAction(version int, a Action) error {
	if a.AgentConnection != nil && (!agentObservationStorageFormat(version) || a.ExecutorKind != "agent" || validateAgentConnection(a.AgentConnection) != nil ||
		a.AgentConnection.CatalogUID != "" && a.AgentConnection.CatalogUID != a.CatalogUID) {
		return ErrAgentActionInvalid
	}
	if a.ExecutorKind != "agent" {
		if a.AgentReceipt != nil {
			return ErrAgentActionInvalid
		}
		return nil
	}
	if !agentActionStorageFormat(version) || (a.Kind != "intervention" && a.Kind != "code") ||
		(a.State != Started && a.State != Unknown && a.State != Succeeded) {
		return ErrAgentActionInvalid
	}
	if r := a.AgentReceipt; r != nil && (r.validate() != nil || r.ObservedAt.Before(a.StartedAt)) {
		return ErrAgentReceiptInvalid
	}
	if a.State == Succeeded && !agentSuccess(a) {
		return ErrAgentReceiptInvalid
	}
	return nil
}

func sameAgentReceipt(a, b AgentReceipt) bool {
	at, bt := a.ObservedAt, b.ObservedAt
	a.ObservedAt, b.ObservedAt = time.Time{}, time.Time{}
	return a == b && at.Equal(bt)
}

func (f *machine) applyAgentReceipt(c Command) Result {
	m, exists := f.image.Monitors[c.MonitorID]
	a, found := m.Actions[c.ActionID]
	if !exists || !found || a.Revision != c.Revision || a.ExecutorKind != "agent" ||
		a.ExecutorSession != c.ExecutorSession || c.ExecutorSession != f.image.LocalExecutorSession ||
		(a.State != Started && a.State != Unknown) || c.At.Before(a.StartedAt) {
		return Result{Err: ErrAgentReceiptConflict}
	}
	r := c.AgentReceipt.Receipt
	if old := a.AgentReceipt; old != nil && sameAgentReceipt(*old, r) {
		return Result{Allowed: true}
	}
	if !a.ExecutorFinishedAt.IsZero() {
		return Result{Err: ErrExecutorUnfenced}
	}
	if old := a.AgentReceipt; old != nil {
		if old.Sequence != c.AgentReceipt.ExpectedSequence || r.ObservedAt.Before(old.ObservedAt) || old.MessageID != "" ||
			old.TaskID != r.TaskID || old.ContextID != "" && old.ContextID != r.ContextID || agentTerminal(old.State) {
			return Result{Err: ErrAgentReceiptConflict}
		}
	} else if c.AgentReceipt.ExpectedSequence != 0 {
		return Result{Err: ErrAgentReceiptConflict}
	}
	m = m.Clone()
	a = a.Clone()
	a.AgentReceipt = &r
	m.Actions[a.ID] = a
	f.image.Version = joinStorageFormat(f.image.Version, AgentActionFormatVersion)
	eventReceipt := r
	eventType := "agent_task_observed"
	if r.MessageID != "" {
		eventType = "agent_message_received"
	}
	event := Event{MonitorID: m.ID, Revision: a.Revision, CatalogUID: a.CatalogUID, IncidentID: a.IncidentID,
		At: c.At, Type: eventType, ActionID: a.ID, Kind: a.Kind, Color: a.Color, Endpoint: a.Endpoint, AgentReceipt: &eventReceipt}
	return Result{Allowed: true, Monitor: &m, Events: []Event{event}}
}

// RecordAgentReceipt commits an observation for this exact invocation before a
// caller polls again. Retry with the same sequence, payload and observation time.
// Neither recording nor replay invokes an agent or resolves an unknown action.
func (h *LocalExecution) RecordAgentReceipt(ctx context.Context, command AgentReceiptCommand) error {
	if h == nil || ctx == nil {
		return ErrAgentReceiptInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	select {
	case h.serial <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-h.serial }()
	if h.completed || h.returnedAt.Load() != nil {
		return ErrExecutorUnfenced
	}
	results, err := h.store.Submit(ctx, []Command{{Kind: "agent_receipt", MonitorID: h.monitorID, Revision: h.revision,
		ActionID: h.actionID, ExecutorSession: h.session, At: command.Receipt.ObservedAt, AgentReceipt: &command}})
	if err != nil {
		return err
	}
	if len(results) != 1 {
		return ErrCommitUnconfirmed
	}
	if results[0].Err != nil {
		return results[0].Err
	}
	if !results[0].Allowed {
		return ErrAgentReceiptConflict
	}
	return nil
}
