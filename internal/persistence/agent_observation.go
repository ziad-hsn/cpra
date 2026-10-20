package persistence

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"math"
	"time"

	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

const AgentObservationFormatVersion = 35
const agentObservationSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-35\n"

// AgentConnectionMaxBytes bounds encrypted connection configuration, not agent
// output or execution parameters. The envelope's wrapping-key bound also applies.
const AgentConnectionMaxBytes = 16 << 10

// AgentConnection keeps only an encrypted original connection and its source
// incarnation. A manifest action may later be adopted by the catalog; its empty
// original incarnation remains part of the authenticated encryption binding.
type AgentConnection struct {
	CatalogUID string                `json:"catalog_uid,omitempty"`
	Payload    secureconfig.Envelope `json:"payload"`
}

func (c AgentConnection) Clone() AgentConnection {
	c.Payload = c.Payload.Clone()
	return c
}

func agentObservationStorageFormat(version int) bool {
	return version == AgentObservationFormatVersion || notificationStorageFormat(version) || version > AgentObservationFormatVersion && externalStorageFormat(version)
}

// AgentConnectionBinding authenticates an immutable action's connection. Keep
// this projection explicit: receipts, reviews, process sessions and mutable
// lifecycle fields must not change the binding after the start commits.
func AgentConnectionBinding(storeID, monitorID string, a Action) secureconfig.Binding {
	uid := a.CatalogUID
	if a.AgentConnection != nil && a.AgentConnection.CatalogUID == "" {
		uid = "" // One-time manifest adoption does not rebind the original connection.
	}
	identityV1 := struct {
		MonitorID  string `json:"monitor_id"`
		MonitorUID string `json:"monitor_uid"`
		ActionID   string `json:"action_id"`
		Revision   string `json:"revision"`
		IncidentID string `json:"incident_id"`
		Kind       string `json:"kind"`
		Color      string `json:"color"`
		Endpoint   int    `json:"endpoint"`
		Attempt    int    `json:"attempt"`
	}{monitorID, uid, a.ID, a.Revision, a.IncidentID, a.Kind, a.Color, a.Endpoint, a.Attempt}
	data, _ := json.Marshal(identityV1)
	return secureconfig.Binding{StoreID: storeID, Kind: "AgentAction", ID: monitorID,
		UID: a.ID, Revision: identity(string(data)), Purpose: "agent-task-connection-v1"}
}

func validateAgentConnection(connection *AgentConnection) error {
	if connection == nil || connection.CatalogUID != "" && !catalogIdentifier(connection.CatalogUID, 256) ||
		connection.Payload.Validate() != nil || len(connection.Payload.Ciphertext) > AgentConnectionMaxBytes+16 {
		return ErrAgentActionInvalid
	}
	return nil
}

// AgentObservationCommand records a GET of the original remote task after the
// sending invocation stops. It grants no start, retry, cancellation or review.
type AgentObservationCommand struct {
	ExpectedRevision string       `json:"expected_revision"`
	ExpectedSequence uint64       `json:"expected_sequence"`
	Receipt          AgentReceipt `json:"receipt"`
}

func validateAgentObservationCommand(c Command) error {
	if c.AgentObservation == nil || !catalogIdentifier(c.MonitorID, 256) || !catalogIdentifier(c.ActionID, 256) ||
		!catalogIdentifier(c.Revision, 256) || !catalogIdentifier(c.ExecutorSession, 256) {
		return ErrAgentReceiptInvalid
	}
	o := c.AgentObservation
	if len(o.ExpectedRevision) != 64 || o.ExpectedSequence == math.MaxUint64 || o.Receipt.Sequence != o.ExpectedSequence+1 ||
		o.Receipt.TaskID == "" || o.Receipt.validate() != nil || !c.At.Equal(o.Receipt.ObservedAt) {
		return ErrAgentReceiptInvalid
	}
	if _, err := hex.DecodeString(o.ExpectedRevision); err != nil {
		return ErrAgentReceiptInvalid
	}
	extra := c
	extra.Kind, extra.MonitorID, extra.ActionID, extra.Revision, extra.ExecutorSession = "", "", "", "", ""
	extra.At, extra.AgentObservation = time.Time{}, nil
	if extra != (Command{}) {
		return ErrAgentReceiptInvalid
	}
	return nil
}

func (f *machine) applyAgentObservation(c Command) Result {
	m, exists := f.image.Monitors[c.MonitorID]
	a, found, err := f.actionForMonitor(m, c.ActionID)
	if err != nil {
		return Result{Err: err}
	}
	if !exists || !found || a.Revision != c.Revision || a.ExecutorKind != "agent" || a.State != Unknown ||
		a.AgentConnection == nil || a.AgentReceipt == nil || a.AgentReceipt.TaskID == "" ||
		c.ExecutorSession != f.image.LocalExecutorSession || c.At.Before(a.StartedAt) {
		return Result{Err: ErrAgentReceiptConflict}
	}
	// Replacement of a local process fences only its invocation, not the remote
	// task. A still-active invocation retains exclusive observation authority.
	if a.ExecutorSession == c.ExecutorSession && a.ExecutorFinishedAt.IsZero() {
		return Result{Err: ErrExecutorUnfenced}
	}
	o := c.AgentObservation
	r := o.Receipt
	if sameAgentReceipt(*a.AgentReceipt, r) {
		return Result{Allowed: true}
	}
	old := a.AgentReceipt
	if ActionReviewRevision(a) != o.ExpectedRevision || old.Sequence != o.ExpectedSequence ||
		r.ObservedAt.Before(old.ObservedAt) || r.TaskID != old.TaskID || old.ContextID != "" && r.ContextID != old.ContextID || agentTerminal(old.State) {
		return Result{Err: ErrAgentReceiptConflict}
	}
	m = m.Clone()
	a = a.Clone()
	a.AgentReceipt = &r
	if m.Actions == nil {
		m.Actions = make(map[string]Action)
	}
	m.Actions[a.ID] = a
	f.image.Version = joinStorageFormat(f.image.Version, AgentObservationFormatVersion)
	eventReceipt := r
	event := Event{MonitorID: m.ID, Revision: a.Revision, CatalogUID: a.CatalogUID, FileIncarnation: a.FileIncarnation, IncidentID: a.IncidentID,
		At: c.At, Type: "agent_task_observed", ActionID: a.ID, Kind: a.Kind, Color: a.Color,
		Endpoint: a.Endpoint, AgentReceipt: &eventReceipt}
	// Unknown state, operator review and incident policy remain unchanged. A
	// terminal observation permits a later conditional review; it is not one.
	return Result{Allowed: true, Monitor: &m, Events: []Event{event}}
}

// RecordAgentObservation commits a read-only task observation against the exact
// action read before network I/O. Callers must authenticate AgentConnection and
// read only its original task. Retry the same original record and receipt after
// an unconfirmed commit; never fetch a newer revision to overwrite a conflict.
func (s *Store) RecordAgentObservation(ctx context.Context, original ActionRecord, receipt AgentReceipt) error {
	if ctx == nil || original.AgentReceipt == nil {
		return ErrAgentReceiptInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := Command{Kind: "agent_observation", MonitorID: original.MonitorID, Revision: original.Revision,
		ActionID: original.ID, ExecutorSession: s.executorSession, At: receipt.ObservedAt,
		AgentObservation: &AgentObservationCommand{ExpectedRevision: original.ReviewRevision,
			ExpectedSequence: original.AgentReceipt.Sequence, Receipt: receipt}}
	results, err := s.Submit(ctx, []Command{command})
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
