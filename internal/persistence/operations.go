package persistence

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const maxPendingCatalogOperations = 4096

var (
	ErrCatalogBusy       = errors.New("pending configuration operations are at capacity")
	ErrOperationNotFound = errors.New("operation not found")
)

// OperationReceipt contains only allowlisted audit/progress information. Active
// receipts live in the FSM; terminal receipts live in retained history segments.
// Configuration ciphertext is never duplicated into the operation ledger.
type OperationReceipt struct {
	InvalidatedByRestore string     `json:"invalidated_by_restore,omitempty"`
	ActionID             string     `json:"action_id,omitempty"`
	Subject              string     `json:"subject,omitempty"`
	IncidentID           string     `json:"incident_id,omitempty"`
	ID                   string     `json:"id"`
	Key                  CatalogKey `json:"key"`
	UID                  string     `json:"uid"`
	OldVersion           string     `json:"old_version,omitempty"`
	NewVersion           string     `json:"new_version"`
	Generation           uint64     `json:"generation"`
	CommittedIndex       uint64     `json:"committed_index"`
	Actor                string     `json:"actor"`
	At                   time.Time  `json:"at"`
	UpdatedAt            time.Time  `json:"updated_at"`
	State                string     `json:"state"`
	Outcome              string     `json:"outcome"`
	Removed              bool       `json:"removed,omitempty"`
}

func validateOperationSubject(subject, incident, action string, key CatalogKey) error {
	if (subject == "recovery" || subject == "review") != (action != "") {
		return errors.New("unexpected operation action")
	}
	switch subject {
	case "":
		if incident != "" {
			return errors.New("unexpected operation incident")
		}
	case "control":
		if key.Kind != "Monitor" || incident != "" {
			return errors.New("invalid control operation")
		}
	case "recovery", "review":
		if key.Kind != "Monitor" || !catalogIdentifier(action, 256) || incident != "" {
			return errors.New("invalid action operation")
		}
	case "incident":
		if key.Kind != "Monitor" || !catalogIdentifier(incident, 256) {
			return errors.New("invalid incident operation")
		}
	default:
		return errors.New("unknown operation subject")
	}
	return nil
}
func (r OperationReceipt) validate() error {
	if r.InvalidatedByRestore != "" && (!validAuthenticationID(r.InvalidatedByRestore) || r.State != "partial" || r.Outcome != "superseded") {
		return errors.New("invalid restored operation receipt")
	}
	unactivated := r.State == "reserved" || r.CommittedIndex == 0 && (r.Outcome == "activation_rejected" || r.Outcome == "reservation_expired" || r.InvalidatedByRestore != "")
	if err := validateOperationSubject(r.Subject, r.IncidentID, r.ActionID, r.Key); err != nil && !(unactivated && r.Subject == "recovery" && r.Key.Kind == "Monitor" && r.ActionID == "" && r.IncidentID == "") {
		return err
	}
	if r.Key.validate() != nil || !catalogIdentifier(r.ID, 256) || !validOperationIdentity(r.ID, r.NewVersion) ||
		!catalogIdentifier(r.UID, 256) || !catalogIdentifier(r.Actor, 128) || r.Generation == 0 ||
		(r.CommittedIndex == 0 && !unactivated) || r.At.IsZero() || r.UpdatedAt.Before(r.At) {
		return errors.New("invalid operation receipt")
	}
	switch r.State {
	case "reserved":
		if r.Outcome != "reserved" || r.CommittedIndex != 0 || !strings.HasPrefix(r.ID, "op.") {
			return errors.New("invalid operation reservation receipt")
		}
	case "committed":
		if r.Outcome != "committed" {
			return errors.New("invalid committed operation outcome")
		}
	case "completed":
		if r.Outcome != "applied" {
			return errors.New("invalid completed operation outcome")
		}
	case "failed":
		if (r.Outcome == "activation_rejected" || r.Outcome == "reservation_expired") && (r.CommittedIndex != 0 || !strings.HasPrefix(r.ID, "op.")) {
			return errors.New("unactivated failure claims a target commit")
		}
		if r.Outcome != "projection_failed" && r.Outcome != "activation_rejected" && r.Outcome != "reservation_expired" {
			return errors.New("invalid failed operation outcome")
		}
	case "partial":
		if r.Outcome != "superseded" {
			return errors.New("invalid superseded operation outcome")
		}
	default:
		return errors.New("invalid operation state")
	}
	return nil
}

// OperationUpdate is emitted by the operational owner after reconciliation, not
// by an HTTP request. Exact incarnation/version checks prevent an old completion
// from claiming the replacement configuration has been installed.
type OperationUpdate struct {
	ActionID   string     `json:"action_id,omitempty"`
	Subject    string     `json:"subject,omitempty"`
	IncidentID string     `json:"incident_id,omitempty"`
	ID         string     `json:"id"`
	Key        CatalogKey `json:"key"`
	UID        string     `json:"uid"`
	Revision   string     `json:"revision"`
	Applied    bool       `json:"applied"`
}

func (u OperationUpdate) validate() error {
	if err := validateOperationSubject(u.Subject, u.IncidentID, u.ActionID, u.Key); err != nil {
		return err
	}
	if u.Key.validate() != nil || !catalogIdentifier(u.ID, 256) || !validOperationIdentity(u.ID, u.Revision) || !catalogIdentifier(u.UID, 256) {
		return errors.New("invalid operation progress identity")
	}
	return nil
}

func receiptEvent(receipt OperationReceipt) Event {
	monitorID := receipt.Key.ID
	if receipt.Key.Kind != "Monitor" {
		monitorID = "resource/" + receipt.Key.Kind + "/" + receipt.Key.ID
	}
	prefix := "configuration"
	if receipt.Subject != "" {
		prefix = receipt.Subject
	}
	event := Event{MonitorID: monitorID, Revision: receipt.NewVersion, At: receipt.UpdatedAt,
		Type: prefix + "_" + receipt.Outcome, Kind: receipt.Key.Kind, Operation: &receipt}
	if receipt.InvalidatedByRestore != "" {
		event.Reason = "explicit_restore"
	}
	return event
}

func (f *machine) applyOperation(update OperationUpdate, at time.Time) Result {
	receipt, ok := f.image.Operations[update.ID]
	if !ok {
		return Result{Err: ErrOperationNotFound}
	}
	if receipt.Key != update.Key || receipt.UID != update.UID || receipt.NewVersion != update.Revision || receipt.Subject != update.Subject || receipt.IncidentID != update.IncidentID || receipt.ActionID != update.ActionID ||
		!operationCurrent(f.image, receipt) || at.Before(receipt.At) {
		return Result{Err: ErrCatalogConflict}
	}
	receipt.UpdatedAt = at
	receipt.State, receipt.Outcome = "failed", "projection_failed"
	if update.Applied {
		receipt.State, receipt.Outcome = "completed", "applied"
	}
	if err := f.persistCollectionChildTerminals([]OperationReceipt{receipt}); err != nil {
		return f.collectionStorageFailure(err)
	}
	f.deleteOperation(update.ID)
	return Result{Allowed: true, Operation: &receipt, Events: []Event{receiptEvent(receipt)}}
}

// Operation reads an active receipt or the latest retained terminal receipt.
// Unknown IDs and expired history never cause an operation to execute again.
func (s *Store) Operation(id string) (OperationReceipt, error) {
	if !catalogIdentifier(id, 256) {
		return OperationReceipt{}, ErrOperationNotFound
	}
	typed := strings.HasPrefix(id, "op.")
	var epoch string
	var seq uint64
	if typed {
		var err error
		epoch, seq, err = ParseOperationHandle(id)
		if err != nil {
			return OperationReceipt{}, ErrOperationNotFound
		}
	}
	if !s.Status().Ready {
		return OperationReceipt{}, fmt.Errorf("%w: durable storage unavailable", ErrHistoryUnavailable)
	}
	s.fsm.mu.RLock()
	currentEpoch, highWater := s.fsm.image.OperationEpoch, s.fsm.image.OperationHighWater
	receipt, ok := s.fsm.image.Operations[id]
	reservation, reserved := s.fsm.image.OperationReservations[id]
	s.fsm.mu.RUnlock()
	if typed && epoch != currentEpoch {
		return OperationReceipt{}, ErrOperationExpired
	}
	if ok {
		return receipt, nil
	}
	if typed && seq > highWater {
		return OperationReceipt{}, ErrOperationNotFound
	}
	if reserved {
		if !time.Now().UTC().Before(reservation.ExpiresAt) {
			return OperationReceipt{}, ErrOperationExpired
		}
		return reservation.OperationReceipt, nil
	}
	receipt, err := s.fsm.history.operation(id)
	if err == nil {
		if receipt.Outcome == "reservation_expired" {
			return OperationReceipt{}, ErrOperationExpired
		}
		return receipt, nil
	}
	if typed && errors.Is(err, ErrOperationNotFound) && seq <= highWater {
		return OperationReceipt{}, ErrOperationExpired
	}
	return OperationReceipt{}, err
}

func operationCurrent(i image, r OperationReceipt) bool {
	if r.Subject == "review" {
		a, ok := i.Monitors[r.Key.ID].Actions[r.ActionID]
		return ok && a.CatalogUID == r.UID && a.Review != nil && a.Review.Revision == r.NewVersion && !a.Review.Conflict
	}
	current, ok := i.Catalog[r.Key.indexKey()]
	if !ok || current.UID != r.UID {
		return false
	}
	if r.Subject == "" {
		return current.Revision == r.NewVersion && current.Generation == r.Generation && current.Removed == r.Removed && current.CommittedIndex == r.CommittedIndex
	}
	m, ok := i.Monitors[r.Key.ID]
	if !ok || m.Removed || current.Removed || m.CatalogUID != r.UID {
		return false
	}
	if r.Subject == "recovery" {
		a, ok := m.Actions[r.ActionID]
		return ok && a.CatalogUID == r.UID && a.OperationID == r.ID && a.State != Cancelled
	}
	if r.Subject == "control" {
		return m.ControlRevision == r.NewVersion
	}
	return m.IncidentID == r.IncidentID && m.IncidentRevision == r.NewVersion && incidentOf(m).Active
}
