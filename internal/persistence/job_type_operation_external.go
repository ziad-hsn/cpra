//go:build externaljobs

package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// JobTypeOperationFormatVersion adds authenticated, reserved JobType mutations
// with terminal configuration receipts. It grants no worker execution rights.
const JobTypeOperationFormatVersion = 16
const jobTypeOperationSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-16\n"

// JobTypeOperationAllocation is a token-free committed authority fence around
// the shared allocator. The ordinary allocation command cannot carry this kind.
type JobTypeOperationAllocation struct {
	Allocation OperationAllocation `json:"allocation"`
	Authority  OperatorAuthority   `json:"authority"`
}

// JobTypeCommitResult observes this exact commit, including terminal rejection
// receipts when returned with an error. State is populated only on success.
type JobTypeCommitResult struct {
	State     JobTypeState
	Operation OperationReceipt
}

// Freeze the original format-15 field order and JSON tags independently of
// later command additions. Both old replay digests and new reservation intent
// hashes use this explicit projection.
type jobTypeCommandDigestV1 struct {
	Action                  string            `json:"action"`
	Value                   JobTypeVersion    `json:"value"`
	ExpectedUID             string            `json:"expected_uid,omitempty"`
	ExpectedRevision        string            `json:"expected_revision,omitempty"`
	RetainedVersionRevision string            `json:"retained_version_revision,omitempty"`
	Authority               OperatorAuthority `json:"authority"`
}

func jobTypeCommandV1(c JobTypeCommand) jobTypeCommandDigestV1 {
	return jobTypeCommandDigestV1{Action: c.Action, Value: c.Value, ExpectedUID: c.ExpectedUID, ExpectedRevision: c.ExpectedRevision, RetainedVersionRevision: c.RetainedVersionRevision, Authority: c.Authority}
}

func operationResourceCommand(kind string) bool { return kind == "catalog" || kind == "job_type" }

func validateOperationReservationExtension(r OperationReservation) error {
	if r.CommandKind != "job_type" || r.Key.Kind != "JobType" || r.Subject != "" || r.IncidentID != "" || r.ActionID != "" || r.Generation == 0 {
		return ErrOperationReservation
	}
	return nil
}

func validateOperationAllocationCommand(a OperationAllocation) error {
	if a.Reservation.CommandKind == "job_type" {
		return ErrOperationReservation
	}
	return a.validate()
}

func (a JobTypeOperationAllocation) validate(at time.Time) error {
	r := a.Allocation.Reservation
	if a.Allocation.validate() != nil || validateOperationReservationExtension(r) != nil || a.Authority.validate() != nil || r.Actor != a.Authority.Actor || !at.Equal(r.At) {
		return ErrOperationReservation
	}
	return nil
}

// jobTypeOperationDigestV1 deliberately uses the frozen format-15 body, without
// the issued handle or fresh admission time. Preparation times and all encrypted
// bytes, CAS guards, selectors and committed authority remain immutable.
func jobTypeOperationReservation(c JobTypeCommand, at time.Time) (OperationReservation, error) {
	if at.IsZero() || at.Year() < 1 || at.Year() > 9999 || at.Before(c.Value.Record.UpdatedAt) {
		return OperationReservation{}, ErrJobTypeInvalid
	}
	c.OperationID = ""
	if err := c.validate(c.Value.Record.UpdatedAt); err != nil {
		return OperationReservation{}, err
	}
	raw, err := json.Marshal(jobTypeCommandV1(c))
	if err != nil {
		return OperationReservation{}, ErrJobTypeInvalid
	}
	digest := sha256.Sum256(append([]byte("cpra-job-type-operation-v1\x00"), raw...))
	v := c.Value.Record
	return OperationReservation{CommandKind: "job_type", Digest: hex.EncodeToString(digest[:]), ExpiresAt: at.Add(OperationReservationLifetime), OperationReceipt: OperationReceipt{
		Key: v.Key, UID: v.UID, OldVersion: c.ExpectedRevision, NewVersion: v.Revision, Generation: v.Generation,
		Actor: c.Authority.Actor, At: at, UpdatedAt: at, State: "reserved", Outcome: "reserved", Removed: v.Removed,
	}}, nil
}

// ReserveJobTypeOperation allocates one original op. handle. An ambiguous
// allocation returns no handle and submits no resource mutation. Callers must
// retain a confirmed handle before invoking CommitJobTypeOperation.
func (s *Store) ReserveJobTypeOperation(ctx context.Context, c JobTypeCommand, at time.Time) (OperationReservation, error) {
	if c.OperationID != "" {
		return OperationReservation{}, ErrOperationReservation
	}
	command := Command{Kind: "job_type", At: c.Value.Record.UpdatedAt, commandExtensions: commandExtensions{JobType: &c}}
	if err := validateCommand(command); err != nil {
		return OperationReservation{}, err
	}
	if _, err := encodedBound(envelope{Version: JobTypeOperationFormatVersion, Commands: []Command{command}}); err != nil {
		return OperationReservation{}, err
	}
	r, err := jobTypeOperationReservation(c, at)
	if err != nil {
		return OperationReservation{}, err
	}
	a := JobTypeOperationAllocation{Allocation: OperationAllocation{Epoch: uuid.NewString(), Reservation: r}, Authority: c.Authority}
	if err := a.validate(at); err != nil {
		return OperationReservation{}, err
	}
	results, err := s.Submit(ctx, []Command{{Kind: "job_type_reserve", At: at, commandExtensions: commandExtensions{JobTypeAllocation: &a}}})
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

func (f *machine) applyJobTypeOperationAllocation(a JobTypeOperationAllocation, at time.Time) Result {
	if err := f.checkOperatorAuthority(a.Authority, at); err != nil {
		return Result{Err: err}
	}
	r := f.applyOperationAllocation(a.Allocation, at)
	if r.Allowed {
		f.image.Version = max(f.image.Version, JobTypeOperationFormatVersion)
	}
	return r
}

func (f *machine) applyJobTypeOperation(c JobTypeCommand, at time.Time, index uint64) Result {
	epoch, sequence, err := ParseOperationHandle(c.OperationID)
	if err != nil {
		return Result{Err: ErrOperationReservation}
	}
	if epoch != f.image.OperationEpoch {
		return Result{Err: ErrOperationExpired}
	}
	reserved, ok := f.image.OperationReservations[c.OperationID]
	if !ok {
		if sequence <= f.image.OperationHighWater {
			return Result{Err: ErrOperationExpired}
		}
		return Result{Err: ErrOperationNotFound}
	}
	expected, err := jobTypeOperationReservation(c, reserved.At)
	expected.ID = c.OperationID
	if err != nil || expected != reserved || at.Before(reserved.At) {
		return Result{Err: ErrOperationReservation}
	}
	r := reserved.OperationReceipt
	r.UpdatedAt = at
	if !at.Before(reserved.ExpiresAt) {
		delete(f.image.OperationReservations, c.OperationID)
		r.State, r.Outcome = "failed", "reservation_expired"
		return Result{Err: ErrOperationExpired, Operation: &r, Events: []Event{receiptEvent(r)}}
	}
	// The body is immutable; the operation identity separates equal bodies
	// belonging to different explicit admissions. No I/O follows installation.
	raw, _ := json.Marshal(c)
	digest := sha256.Sum256(append([]byte("cpra-job-type-commit-v1\x00"), raw...))
	result := f.applyJobType(c, at, index, hex.EncodeToString(digest[:]))
	delete(f.image.OperationReservations, c.OperationID)
	if result.Allowed {
		r.State, r.Outcome, r.CommittedIndex = "completed", "applied", index
	} else {
		r.State, r.Outcome = "failed", "activation_rejected"
	}
	result.Operation = &r
	result.Events = append(result.Events, receiptEvent(r))
	return result
}

// CommitJobTypeOperation consumes exactly one authenticated reservation. It
// never allocates, retries, or reconstructs a lost command. Read OperationContext
// with the retained handle to reconcile an uncertain response.
func (s *Store) CommitJobTypeOperation(ctx context.Context, c JobTypeCommand, at time.Time) (JobTypeCommitResult, error) {
	if _, _, err := ParseOperationHandle(c.OperationID); err != nil {
		return JobTypeCommitResult{}, ErrOperationReservation
	}
	results, err := s.Submit(ctx, []Command{{Kind: "job_type", At: at, commandExtensions: commandExtensions{JobType: &c}}})
	if err != nil {
		return JobTypeCommitResult{}, err
	}
	if len(results) != 1 {
		return JobTypeCommitResult{}, errors.Join(ErrCommitUnconfirmed, ErrJobTypeUnavailable)
	}
	r := results[0]
	var out JobTypeCommitResult
	if r.Operation != nil {
		out.Operation = *r.Operation
	}
	if r.JobType != nil {
		out.State = *r.JobType
	}
	if r.Err != nil {
		return out, r.Err
	}
	if !r.Allowed || r.Operation == nil || r.JobType == nil {
		return out, errors.Join(ErrCommitUnconfirmed, ErrJobTypeUnavailable)
	}
	return out, nil
}
