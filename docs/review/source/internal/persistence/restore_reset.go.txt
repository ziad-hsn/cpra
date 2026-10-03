package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/google/uuid"
)

const restoredIdentityVersion = 3
const restoreMarkerFile = "RESTORE.json"
const restorePageSize = 256

// Each action can also invalidate its recovery and review receipts. Reserve
// three audit records per action so one page stays below 256 retained events.
const restoreActionPageSize = restorePageSize / 3

var ErrRestorePending = errors.New("explicit restore reset is incomplete")
var ErrRestoreInvalid = errors.New("invalid or incompatible restore reset identity")

// RestoreMarker is nonsecret and retained beside the log. The identity file
// pins its ID and rejects older binaries before they can ignore the reset.
// NodeID stays unchanged because encrypted catalog records are bound to it.
type RestoreMarker struct {
	Version                int       `json:"version"`
	NodeID                 string    `json:"node_id"`
	ID                     string    `json:"id"`
	AuthenticationEpoch    string    `json:"authentication_epoch"`
	AuthenticationRevision string    `json:"authentication_revision"`
	OperationEpoch         string    `json:"operation_epoch"`
	At                     time.Time `json:"at"`
}

type RestoreState struct {
	Marker RestoreMarker `json:"marker"`
	Phase  string        `json:"phase"`
	After  string        `json:"after,omitempty"`
}

type RestoreCommand struct {
	Marker RestoreMarker `json:"marker"`
	Phase  string        `json:"phase"`
	After  string        `json:"after,omitempty"`
}

func (m RestoreMarker) validate() error {
	if m.Version != 1 || m.At.IsZero() || m.At.Year() < 1 || m.At.Year() > 9999 {
		return ErrRestoreInvalid
	}
	seen := make(map[string]bool, 5)
	for _, id := range []string{m.NodeID, m.ID, m.AuthenticationEpoch, m.AuthenticationRevision, m.OperationEpoch} {
		u, err := uuid.Parse(id)
		if err != nil || u == uuid.Nil || u.String() != id || seen[id] {
			return ErrRestoreInvalid
		}
		seen[id] = true
	}
	return nil
}

func (c RestoreCommand) validate() error {
	if err := c.Marker.validate(); err != nil {
		return err
	}
	switch c.Phase {
	case "begin", "receipts":
		if c.After != "" {
			return ErrRestoreInvalid
		}
	case "actions":
		if c.After != "" && !catalogIdentifier(c.After, 256) {
			return ErrRestoreInvalid
		}
	default:
		return ErrRestoreInvalid
	}
	return nil
}

// MarkRestored validates/locks the complete private restore destination before
// recording its new identity. It must be called before publishing that directory.
// It starts no Raft process and writes no provider or credential material.
func MarkRestored(directory string, at time.Time) error {
	lock, err := LockOffline(directory)
	if err != nil {
		return err
	}
	defer lock.Close()
	m := RestoreMarker{Version: 1, NodeID: lock.NodeID, ID: uuid.NewString(), AuthenticationEpoch: uuid.NewString(),
		AuthenticationRevision: uuid.NewString(), OperationEpoch: uuid.NewString(), At: at}
	if err = m.validate(); err != nil {
		return err
	}
	// Publish the incompatibility fence first. If the marker write fails, both
	// old and current binaries reject the incomplete destination.
	node := nodeIdentity{Version: restoredIdentityVersion, ID: lock.NodeID, Initialized: true, RestoreID: m.ID}
	if err = atomicJSON(filepath.Join(directory, "identity.json"), node); err != nil {
		return err
	}
	return atomicJSON(filepath.Join(directory, restoreMarkerFile), m)
}

func readRestoreMarker(directory string, node nodeIdentity) (*RestoreMarker, error) {
	path := filepath.Join(directory, restoreMarkerFile)
	info, err := os.Lstat(path)
	if node.Version == FormatVersion {
		if node.RestoreID != "" || !errors.Is(err, os.ErrNotExist) {
			return nil, ErrRestoreInvalid
		}
		return nil, nil
	}
	if node.Version != restoredIdentityVersion || err != nil || !info.Mode().IsRegular() || info.Size() > 4096 {
		return nil, ErrRestoreInvalid
	}
	var marker RestoreMarker
	if err := readOfflineJSON(path, &marker); err != nil || marker.validate() != nil || marker.NodeID != node.ID || marker.ID != node.RestoreID {
		return nil, ErrRestoreInvalid
	}
	return &marker, nil
}

func (f *machine) restorePending() bool {
	return f.image.Restore != nil && f.image.Restore.Phase != "complete"
}

func validateRestoreImage(i image) error {
	r := i.Restore
	if r == nil {
		return nil
	}
	if !catalogFormat(i.Version) || r.Marker.validate() != nil || i.Authentication == nil ||
		i.Authentication.RestoreID != r.Marker.ID || i.Authentication.Epoch != r.Marker.AuthenticationEpoch || i.OperationEpoch != r.Marker.OperationEpoch ||
		i.Authentication.UpdatedAt.Before(r.Marker.At) || (i.Authentication.Revision == r.Marker.AuthenticationRevision) != i.Authentication.ResetRequired {
		return ErrRestoreInvalid
	}
	switch r.Phase {
	case "actions":
		if !i.Authentication.ResetRequired || r.After != "" && !catalogIdentifier(r.After, 256) {
			return ErrRestoreInvalid
		}
	case "receipts":
		if !i.Authentication.ResetRequired || r.After != "" {
			return ErrRestoreInvalid
		}
	case "complete":
		if r.After != "" {
			return ErrRestoreInvalid
		}
	default:
		return ErrRestoreInvalid
	}
	if i.Authentication.ResetRequired {
		// A corrupt checkpoint cannot skip unfenced work and then claim the
		// reset is complete. After explicit provisioning, new ordinary work is
		// permitted; this invariant applies only while reset owns admission.
		for _, monitor := range i.Monitors {
			for _, action := range monitor.Actions {
				if (action.State == Queued || action.State == Started) && (r.Phase != "actions" || action.ID <= r.After) {
					return ErrRestoreInvalid
				}
			}
		}
		if r.Phase == "complete" && len(i.Operations)+len(i.OperationReservations) != 0 {
			return ErrRestoreInvalid
		}
	}
	return nil
}

func (f *machine) applyRestore(c RestoreCommand) Result {
	r := f.image.Restore
	if c.Phase == "begin" {
		if r != nil && r.Marker == c.Marker {
			return Result{Allowed: true}
		}
		if r != nil && (r.Marker.ID == c.Marker.ID || r.Phase != "complete") {
			return Result{Err: ErrRestoreInvalid}
		}
		data, _ := json.Marshal(c.Marker)
		f.image.Authentication = &AuthenticationState{Version: AuthenticationFormatVersion, Epoch: c.Marker.AuthenticationEpoch,
			Revision: c.Marker.AuthenticationRevision, BootstrapConsumed: true, ResetRequired: true, UpdatedAt: c.Marker.At,
			CommandDigest: identity("authentication-restore/v1/" + string(data)), RestoreID: c.Marker.ID}
		workerEvents := f.resetWorkerPolicy(c.Marker)
		if f.err != nil {
			return Result{Err: f.err}
		}
		f.image.OperationEpoch = c.Marker.OperationEpoch
		f.image.OperationHighWater = 0
		f.image.CollectionAdmissions = nil
		f.image.CollectionAdmissionWatermark = time.Time{}
		f.image.Restore = &RestoreState{Marker: c.Marker, Phase: "actions"}
		f.image.Version = max(f.image.Version, CatalogFormatVersion)
		return Result{Allowed: true, Events: append(workerEvents, Event{MonitorID: "authentication", Revision: c.Marker.AuthenticationRevision,
			At: c.Marker.At, Type: "authentication_reset", Kind: "authentication", Actor: "local-administrator", Reason: "explicit_restore", Outcome: "provision_required"})}
	}
	if r == nil || r.Marker != c.Marker || r.Phase != c.Phase || r.After != c.After {
		return Result{Err: ErrRestoreInvalid}
	}
	next := *r
	var result Result
	if c.Phase == "actions" {
		result = f.restoreActions(&next)
	} else {
		result = f.restoreReceipts(&next)
	}
	if result.Allowed && f.err == nil {
		f.image.Restore = &next
	}
	return result
}

func restoreReceiptEvent(receipt OperationReceipt, marker RestoreMarker) Event {
	receipt.State, receipt.Outcome = "partial", "superseded"
	receipt.InvalidatedByRestore = marker.ID
	receipt.UpdatedAt = marker.At
	if receipt.UpdatedAt.Before(receipt.At) {
		receipt.UpdatedAt = receipt.At
	}
	return receiptEvent(receipt)
}

func (f *machine) restoreActions(state *RestoreState) Result {
	if f.actionIndex == nil {
		f.rebuildActionIndexes()
	}
	page := make([]actionItem, 0, restoreActionPageSize)
	f.actionIndex.AscendGreaterOrEqual(actionItem{key: state.After}, func(item actionItem) bool {
		if item.key == state.After {
			return true
		}
		page = append(page, item)
		return len(page) < restoreActionPageSize
	})
	var events []Event
	changed := make(map[string]Monitor)
	for _, item := range page {
		state.After = item.key
		if item.action.State != Queued && item.action.State != Started {
			continue
		}
		m, exists := changed[item.monitorID]
		if !exists {
			m = f.image.Monitors[item.monitorID].Clone()
		}
		a := m.Actions[item.key]
		typ := "action_cancelled"
		a.State, a.Outcome = Cancelled, "explicit_restore"
		if item.action.State == Started {
			a.State, a.Outcome, typ = Unknown, "explicit_restore_while_started", "action_unknown"
		}
		a.FinishedAt = state.Marker.At
		if a.FinishedAt.Before(a.StartedAt) {
			a.FinishedAt = a.StartedAt
		}
		m.Actions[item.key] = a
		changed[item.monitorID] = m
		events = append(events, Event{MonitorID: m.ID, Revision: a.Revision, At: a.FinishedAt, Type: typ,
			ActionID: a.ID, IncidentID: a.IncidentID, CatalogUID: a.CatalogUID, Kind: a.Kind, Color: a.Color, Endpoint: a.Endpoint,
			Outcome: a.Outcome, Reason: "explicit_restore", Actor: "local-administrator"})
	}
	ids := make([]string, 0, len(changed))
	for id := range changed {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		installed := f.installMonitor(f.image.Monitors[id], changed[id], state.Marker.At)
		for _, event := range installed {
			if event.Operation != nil {
				event = restoreReceiptEvent(*event.Operation, state.Marker)
			}
			events = append(events, event)
		}
	}
	if len(page) < restoreActionPageSize {
		state.Phase, state.After = "receipts", ""
	}
	return Result{Allowed: true, Events: events}
}

func (f *machine) restoreReceipts(state *RestoreState) Result {
	// Inactive uploads are never resumed after an explicit backup restoration.
	// Keep their original encrypted inventory for consistent snapshot validation;
	// a later retention pass may remove it, independently of active mutations.
	var collectionEvents []Event
	invalidated := make(map[string]CollectionState)
	collectionIDs := make([]string, 0, len(f.image.Collections))
	for id := range f.image.Collections {
		collectionIDs = append(collectionIDs, id)
	}
	slices.Sort(collectionIDs)
	for _, id := range collectionIDs {
		collection := f.image.Collections[id]
		if collectionLive(collection.Phase) {
			collection.Phase, collection.TerminalAt, collection.InvalidatedByRestore = "invalidated", state.Marker.At, state.Marker.ID
			if collection.TerminalAt.Before(collection.ActivityAt) {
				collection.TerminalAt = collection.ActivityAt
			}
			if collection.Activation != nil && collection.TerminalAt.Before(collection.Activation.At) {
				collection.TerminalAt = collection.Activation.At
			}
			invalidated[id] = collection
			collectionEvents = append(collectionEvents, collectionReceiptEvent(collectionReceiptFor(collection)))
		}
	}
	ids := make([]string, 0, f.pendingOperationCount())
	for id := range f.image.Operations {
		ids = append(ids, id)
	}
	for id := range f.image.OperationReservations {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	if limit := restorePageSize - len(collectionEvents); len(ids) > limit {
		ids = ids[:limit]
	}
	events := make([]Event, 0, len(ids)+len(collectionEvents))
	events = append(events, collectionEvents...)
	terminals := make([]OperationReceipt, 0, len(ids))
	for _, id := range ids {
		receipt, ok := f.image.Operations[id]
		if !ok {
			receipt = f.image.OperationReservations[id].OperationReceipt
		}
		event := restoreReceiptEvent(receipt, state.Marker)
		events = append(events, event)
		terminals = append(terminals, *event.Operation)
	}
	// No parent invalidation, cursor advance or receipt deletion is visible if
	// retained child evidence cannot be committed. This page is one ledger tx.
	if err := f.persistCollectionChildTerminals(terminals); err != nil {
		return f.collectionStorageFailure(err)
	}
	for id, collection := range invalidated {
		// A child terminal may have advanced the parent's execution commitment
		// in this same transaction. Preserve it when installing invalidation.
		collection.Execution = f.image.Collections[id].Execution
		f.image.Collections[id] = collection
		f.discardCollectionPlanPrefix(id)
		f.discardCollectionValidationPlan(id)
	}
	for _, id := range ids {
		f.deleteOperation(id)
		delete(f.image.OperationReservations, id)
	}
	if f.pendingOperationCount() == 0 {
		state.Phase = "complete"
	}
	return Result{Allowed: true, Events: events}
}

func (s *Store) finishRestore(ctx context.Context) error {
	if s.restoreMarker == nil {
		if s.fsm.image.Restore != nil {
			return ErrRestoreInvalid
		}
		return nil
	}
	for {
		s.fsm.mu.RLock()
		state := s.fsm.image.Restore
		command := RestoreCommand{Marker: *s.restoreMarker, Phase: "begin"}
		if state != nil && state.Marker == *s.restoreMarker {
			if state.Phase == "complete" {
				s.fsm.mu.RUnlock()
				return nil
			}
			command.Phase, command.After = state.Phase, state.After
		}
		s.fsm.mu.RUnlock()
		results, err := s.Submit(ctx, []Command{{Kind: "restore_reset", At: command.Marker.At, Restore: &command}})
		if err != nil {
			return err
		}
		if len(results) != 1 || results[0].Err != nil || !results[0].Allowed {
			if len(results) == 1 && results[0].Err != nil {
				return results[0].Err
			}
			return fmt.Errorf("%w: reset commit was not confirmed", ErrRestoreInvalid)
		}
	}
}

func (s *Store) OperationEpoch() (string, error) {
	if err := s.authenticationReadReady(); err != nil {
		return "", err
	}
	s.fsm.mu.RLock()
	defer s.fsm.mu.RUnlock()
	if s.fsm.err != nil {
		return "", ErrAuthenticationUnavailable
	}
	return s.fsm.image.OperationEpoch, nil
}
