package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	OperationReservationLifetime = 24 * time.Hour
	operationRetirementBatch     = 256
)

var (
	ErrOperationExpired     = errors.New("operation is expired or belongs to another storage epoch")
	ErrOperationReservation = errors.New("operation reservation does not match this mutation")
	// No target mutation is submitted when allocation is unconfirmed. This is
	// deliberately distinct from uncertainty about an admitted target mutation.
	ErrOperationAllocationUnconfirmed = errors.New("operation allocation is unconfirmed; no target mutation was submitted")
)

// OperationReservation retains bounded nonsecret identities and a digest, never
// the prepared payload. Its receipt reports zero target commits until activation.
type OperationReservation struct {
	OperationReceipt
	CommandKind string    `json:"command_kind"`
	Digest      string    `json:"digest"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// OperationAllocation is internal durable allocation input. Epoch is a proposed
// initial epoch; an already initialized machine always uses its retained epoch.
type OperationAllocation struct {
	Epoch       string               `json:"epoch"`
	Reservation OperationReservation `json:"reservation"`
}

// ParseOperationHandle accepts only the canonical server-issued representation.
// Legacy operation identifiers are handled separately by retained-record lookup.
func ParseOperationHandle(id string) (string, uint64, error) {
	if len(id) != 60 || id[:3] != "op." || id[39] != '.' {
		return "", 0, ErrOperationNotFound
	}
	epoch := id[3:39]
	u, err := uuid.Parse(epoch)
	if err != nil || u == uuid.Nil || u.String() != epoch {
		return "", 0, ErrOperationNotFound
	}
	for _, c := range id[40:] {
		if c < '0' || c > '9' {
			return "", 0, ErrOperationNotFound
		}
	}
	sequence, err := strconv.ParseUint(id[40:], 10, 64)
	if err != nil || sequence == 0 {
		return "", 0, ErrOperationNotFound
	}
	return epoch, sequence, nil
}

func operationHandle(epoch string, sequence uint64) string {
	return fmt.Sprintf("op.%s.%020d", epoch, sequence)
}
func validOperationIdentity(id, revision string) bool {
	if strings.HasPrefix(id, "op.") {
		_, _, err := ParseOperationHandle(id)
		return err == nil && catalogIdentifier(revision, 256) && id != revision
	}
	return catalogIdentifier(id, 256) && id == revision
}
func validOperationEpoch(epoch string) bool {
	u, err := uuid.Parse(epoch)
	return err == nil && u != uuid.Nil && u.String() == epoch
}

// operationTarget copies only the operation-bearing command body before clearing
// the handle. All other payload fields, including guards, encrypted content, actor,
// and policy, remain bound to the reservation digest.
func operationTarget(c Command) (Command, OperationReceipt, string, error) {
	r := OperationReceipt{At: c.At, UpdatedAt: c.At, State: "reserved", Outcome: "reserved"}
	var id string
	switch c.Kind {
	case "catalog":
		if c.Catalog == nil {
			return c, r, "", ErrOperationReservation
		}
		body := *c.Catalog
		id, body.OperationID = body.OperationID, ""
		r.Key, r.UID, r.OldVersion, r.NewVersion = body.Record.Key, body.Record.UID, body.ExpectedRevision, body.Record.Revision
		r.Generation, r.Actor, r.Removed = body.Record.Generation, body.Actor, body.Record.Removed
		c.Catalog = &body
	case "control":
		if c.Control == nil {
			return c, r, "", ErrOperationReservation
		}
		body := *c.Control
		id, body.OperationID = body.OperationID, ""
		r.Subject, r.IncidentID = body.Subject(), body.IncidentID
		r.Key, r.UID, r.OldVersion, r.NewVersion, r.Actor = CatalogKey{Kind: "Monitor", ID: c.MonitorID}, body.MonitorUID, body.ExpectedRevision, body.Revision, body.Actor
		c.Control = &body
	case "action_review":
		if c.ActionReview == nil {
			return c, r, "", ErrOperationReservation
		}
		body := *c.ActionReview
		id, body.OperationID = body.OperationID, ""
		r.Subject, r.ActionID = "review", body.ActionID
		r.Key, r.UID, r.OldVersion, r.NewVersion, r.Actor = CatalogKey{Kind: "Monitor", ID: c.MonitorID}, body.MonitorUID, body.ExpectedRevision, body.Revision, body.Actor
		c.ActionReview = &body
	case "manual_recovery":
		if c.ManualRecovery == nil {
			return c, r, "", ErrOperationReservation
		}
		body := *c.ManualRecovery
		id, body.OperationID = body.OperationID, ""
		r.Subject = "recovery"
		r.Key, r.UID, r.OldVersion, r.NewVersion, r.Actor = CatalogKey{Kind: "Monitor", ID: c.MonitorID}, body.MonitorUID, body.ExpectedControlRevision, body.Revision, body.Actor
		c.ManualRecovery = &body
	default:
		return c, r, "", ErrOperationReservation
	}
	return c, r, id, nil
}
func operationDigest(c Command) (OperationReservation, string, error) {
	if hasCommandExtensions(c) {
		return OperationReservation{}, "", ErrOperationReservation
	}
	normalized, r, id, err := operationTarget(c)
	if err != nil {
		return OperationReservation{}, "", err
	}
	normalized.At = time.Time{}
	data, err := operationDigestEncoding(normalized)
	if err != nil {
		return OperationReservation{}, "", ErrOperationReservation
	}
	h := sha256.Sum256(data)
	return OperationReservation{OperationReceipt: r, CommandKind: c.Kind, Digest: hex.EncodeToString(h[:]), ExpiresAt: c.At.Add(OperationReservationLifetime)}, id, nil
}
func (a OperationAllocation) validate() error {
	r := a.Reservation
	// IDs, generation and target commit position are assigned by the FSM.
	if !validOperationEpoch(a.Epoch) || r.ID != "" || r.Generation != 0 && !operationResourceCommand(r.CommandKind) || r.CommittedIndex != 0 ||
		r.UpdatedAt != r.At || r.At.IsZero() || r.State != "reserved" || r.Outcome != "reserved" || r.InvalidatedByRestore != "" ||
		(r.OldVersion != "" && !catalogIdentifier(r.OldVersion, 256)) || (!operationResourceCommand(r.CommandKind) && r.Removed) ||
		!catalogIdentifier(r.NewVersion, 256) || !catalogIdentifier(r.UID, 256) || !catalogIdentifier(r.Actor, 128) || r.Key.validate() != nil ||
		!r.ExpiresAt.Equal(r.At.Add(OperationReservationLifetime)) || len(r.Digest) != 64 {
		return ErrOperationReservation
	}
	if _, err := hex.DecodeString(r.Digest); err != nil {
		return ErrOperationReservation
	}
	switch r.CommandKind {
	case "catalog":
		if r.Subject != "" || r.Generation == 0 || r.IncidentID != "" || r.ActionID != "" {
			return ErrOperationReservation
		}
	case "control":
		if (r.Subject != "control" && r.Subject != "incident") || validateOperationSubject(r.Subject, r.IncidentID, "", r.Key) != nil || r.ActionID != "" {
			return ErrOperationReservation
		}
	case "manual_recovery":
		if r.Subject != "recovery" || r.Key.Kind != "Monitor" || r.IncidentID != "" || r.ActionID != "" {
			return ErrOperationReservation
		}
	case "action_review":
		if r.Subject != "review" || validateOperationSubject(r.Subject, "", r.ActionID, r.Key) != nil || r.IncidentID != "" {
			return ErrOperationReservation
		}
	default:
		return validateOperationReservationExtension(r)
	}
	return nil
}

// ReserveOperation allocates only a handle and immutable intent identity. The
// caller must submit the same body with the returned ID and a fresh At, and must not retry
// allocation or activation automatically. Manual limits are copied from this
// store, exactly as RequestRecovery does on activation.
func (s *Store) ReserveOperation(ctx context.Context, c Command) (OperationReservation, error) {
	if c.ManualRecovery != nil {
		body := *c.ManualRecovery
		body.Limits = s.config.ManualRecovery.Effective()
		c.ManualRecovery = &body
	}
	if err := validateCommand(c); err != nil {
		return OperationReservation{}, err
	}
	// Bound encoded preparation before hashing even though only metadata persists.
	if _, err := encodedBound(envelope{Version: CatalogFormatVersion, Commands: []Command{c}}); err != nil {
		return OperationReservation{}, err
	}
	r, _, err := operationDigest(c)
	if err != nil {
		return OperationReservation{}, err
	}
	a := OperationAllocation{Epoch: uuid.NewString(), Reservation: r}
	if err = a.validate(); err != nil {
		return OperationReservation{}, err
	}
	results, err := s.Submit(ctx, []Command{{Kind: "operation_reserve", At: c.At, OperationAllocation: &a}})
	if err != nil {
		if errors.Is(err, ErrCommitUnconfirmed) {
			return OperationReservation{}, ErrOperationAllocationUnconfirmed
		}
		return OperationReservation{}, err
	}
	if len(results) != 1 {
		return OperationReservation{}, ErrOperationAllocationUnconfirmed
	}
	if results[0].Err != nil {
		return OperationReservation{}, results[0].Err
	}
	if !results[0].Allowed || results[0].Reservation == nil {
		return OperationReservation{}, ErrOperationAllocationUnconfirmed
	}
	return *results[0].Reservation, nil
}

func (f *machine) applyOperationAllocation(a OperationAllocation, at time.Time) Result {
	events := f.expireOperationReservations(at)
	if f.pendingOperationCount() >= maxPendingCatalogOperations {
		return Result{Err: ErrCatalogBusy, Events: events}
	}
	if f.image.OperationHighWater == math.MaxUint64 {
		return Result{Err: ErrCatalogBusy, Events: events}
	}
	if f.image.OperationEpoch == "" {
		f.image.OperationEpoch = a.Epoch
	}
	f.image.OperationHighWater++
	r := a.Reservation
	r.ID = operationHandle(f.image.OperationEpoch, f.image.OperationHighWater)
	if r.Generation == 0 {
		r.Generation = max(1, f.image.Catalog[r.Key.indexKey()].Generation)
	}
	if f.image.OperationReservations == nil {
		f.image.OperationReservations = make(map[string]OperationReservation)
	}
	f.image.OperationReservations[r.ID] = r
	f.image.Version = max(f.image.Version, CatalogFormatVersion)
	return Result{Allowed: true, Reservation: &r, Operation: &r.OperationReceipt, Events: events}
}

// ExpireOperationReservations retires at most 256 inactive reservations. Active
// committed operations never expire through this path. Allocation also performs
// one such bounded sweep, keeping a full abandoned queue recoverable.
func (s *Store) ExpireOperationReservations(ctx context.Context, at time.Time) ([]Result, error) {
	return s.Submit(ctx, []Command{{Kind: "operation_expire", At: at}})
}
func (f *machine) expireOperationReservations(at time.Time) []Event {
	ids := make([]string, 0, len(f.image.OperationReservations))
	for id, r := range f.image.OperationReservations {
		if !at.Before(r.ExpiresAt) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	if len(ids) > operationRetirementBatch {
		ids = ids[:operationRetirementBatch]
	}
	events := make([]Event, 0, len(ids))
	for _, id := range ids {
		r := f.image.OperationReservations[id].OperationReceipt
		r.State, r.Outcome, r.UpdatedAt = "failed", "reservation_expired", at
		delete(f.image.OperationReservations, id)
		events = append(events, receiptEvent(r))
	}
	return events
}

func (f *machine) applyManagementTarget(c Command, index uint64, format int) Result {
	switch c.Kind {
	case "catalog":
		r := f.applyCatalog(*c.Catalog, index, c.At, format)
		if r.Allowed && r.Catalog != nil {
			r.Events = append(r.Events, f.supersedeCatalogControls(*r.Catalog, c.At)...)
		}
		return r
	case "control":
		return f.applyControl(c.MonitorID, *c.Control, c.At, index)
	default:
		return f.applyRecoveryReview(c, index)
	}
}
func (f *machine) applyOperationTarget(c Command, index uint64, format int) Result {
	_, _, id, err := operationTarget(c)
	if err != nil {
		return Result{Err: err}
	}
	if !strings.HasPrefix(id, "op.") {
		return f.applyManagementTarget(c, index, format)
	} // replay/internal legacy compatibility
	epoch, seq, err := ParseOperationHandle(id)
	if err != nil {
		return Result{Err: ErrOperationReservation}
	}
	if epoch != f.image.OperationEpoch {
		return Result{Err: ErrOperationExpired}
	}
	reservation, ok := f.image.OperationReservations[id]
	if !ok {
		if seq <= f.image.OperationHighWater {
			return Result{Err: ErrOperationExpired}
		}
		return Result{Err: ErrOperationNotFound}
	}
	prepared, _, err := operationDigest(c)
	if err != nil || prepared.Digest != reservation.Digest || c.At.Before(reservation.At) {
		return Result{Err: ErrOperationReservation}
	}
	if !c.At.Before(reservation.ExpiresAt) {
		r := reservation.OperationReceipt
		r.State, r.Outcome, r.UpdatedAt = "failed", "reservation_expired", c.At
		delete(f.image.OperationReservations, id)
		return Result{Err: ErrOperationExpired, Operation: &r, Events: []Event{receiptEvent(r)}}
	}
	// Versions identify target state independently of the handle. Reject a
	// duplicate pending tuple before mutating, rather than poisoning an index
	// or writing a snapshot which cannot restore.
	if _, exists := f.operationByVersion(reservation.Key, reservation.UID, reservation.Subject, reservation.NewVersion); exists {
		r := reservation.OperationReceipt
		r.State, r.Outcome, r.UpdatedAt = "failed", "activation_rejected", c.At
		delete(f.image.OperationReservations, id)
		return Result{Err: ErrOperationReservation, Operation: &r, Events: []Event{receiptEvent(r)}}
	}
	// Activation time is supplied by admission, never generated during replay.
	// Catalog preparation retains its reservation through all fallible work;
	// only the no-I/O installation consumes that capacity. Other target kinds
	// retain their existing lifecycle and are never linked collection children.
	var result Result
	if c.Kind == "catalog" {
		p, err := f.prepareCatalogMutationConsuming(*c.Catalog, index, c.At, format, id)
		if err != nil {
			result = Result{Err: err}
		} else {
			if err := f.persistCollectionChildTerminals(collectionCatalogTerminals(p)); err != nil {
				// Storage failure must retain the reservation. It is not a
				// business rejection and must not publish activation_rejected.
				return f.collectionStorageFailure(err)
			}
			delete(f.image.OperationReservations, id)
			result = f.installCatalogMutation(p)
			result.Events = append(result.Events, f.supersedeCatalogControls(*result.Catalog, c.At)...)
		}
	} else {
		delete(f.image.OperationReservations, id)
		result = f.applyManagementTarget(c, index, format)
	}
	if !result.Allowed {
		delete(f.image.OperationReservations, id)
		r := reservation.OperationReceipt
		r.State, r.Outcome, r.UpdatedAt = "failed", "activation_rejected", c.At
		result.Operation = &r
		result.Events = append(result.Events, receiptEvent(r))
	}
	return result
}

func validateOperationAllocationImage(i image) error {
	if i.OperationEpoch != "" && !validOperationEpoch(i.OperationEpoch) || i.OperationEpoch == "" && (i.OperationHighWater != 0 || len(i.OperationReservations) != 0) {
		return errors.New("invalid operation allocation epoch")
	}
	if (i.OperationHighWater != 0 || len(i.OperationReservations) != 0) && !catalogFormat(i.Version) {
		return errors.New("operation allocation requires catalog format")
	}
	restoring := i.Restore != nil && i.Restore.Phase != "complete"
	checkHandle := func(id string) error {
		epoch, seq, err := ParseOperationHandle(id)
		if err != nil || !restoring && (epoch != i.OperationEpoch || seq > i.OperationHighWater) {
			return ErrOperationReservation
		}
		return nil
	}
	for id, r := range i.OperationReservations {
		if id != r.ID || r.State != "reserved" || r.validate() != nil || checkHandle(id) != nil {
			return errors.New("invalid retained operation reservation")
		}
		if _, exists := i.Operations[id]; exists {
			return errors.New("reservation is already activated")
		}
		prepared := r
		prepared.ID = ""
		if !operationResourceCommand(r.CommandKind) {
			prepared.Generation = 0
		}
		if (OperationAllocation{Epoch: i.OperationEpoch, Reservation: prepared}).validate() != nil {
			return errors.New("invalid operation reservation metadata")
		}
	}
	versions := make(map[operationVersionKey]bool, len(i.Operations))
	for id, r := range i.Operations {
		if strings.HasPrefix(id, "op.") && checkHandle(id) != nil {
			return errors.New("pending operation has an unissued handle")
		}
		key := operationVersion(r)
		if versions[key] {
			return errors.New("duplicate pending operation version")
		}
		versions[key] = true
	}
	return nil
}

// Maintenance performs no write when no expired reservation exists. One bounded
// pass prevents abandoned reservations from indefinitely starving owner receipts.
func (s *Store) maintainOperationReservations(at time.Time) error {
	s.fsm.mu.RLock()
	expired := false
	for _, r := range s.fsm.image.OperationReservations {
		if !at.Before(r.ExpiresAt) {
			expired = true
			break
		}
	}
	s.fsm.mu.RUnlock()
	if !expired {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	results, err := s.ExpireOperationReservations(ctx, at)
	if err != nil {
		return fmt.Errorf("operation reservation maintenance: %w", err)
	}
	if len(results) != 1 || !results[0].Allowed || results[0].Err != nil {
		return errors.New("operation reservation maintenance failed")
	}
	return nil
}
