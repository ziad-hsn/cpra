//go:build externaljobs

package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const WorkerCheckRetentionFormatVersion = 27
const workerCheckRetentionSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-27\n"

const (
	MaxWorkerCheckReservations     = 1024
	MaxWorkerCheckReservationBytes = 1 << 20
	MaxWorkerCheckReceipts         = 1024
	MaxWorkerCheckRetirements      = 128
	WorkerCheckReservationLifetime = time.Minute
	WorkerCheckReceiptLifetime     = 5 * time.Minute
	workerCheckImageOverhead       = 4096
)

// ErrWorkerCheckExpired means an issued check identity has been retired. It
// does not describe a retained outcome or authorize another execution.
var ErrWorkerCheckExpired = errors.New("worker check record expired")

type workerCheckReservation struct {
	OwnerEpoch     string    `json:"owner_epoch"`
	CreatedAt      time.Time `json:"created_at"`
	CommittedIndex uint64    `json:"committed_index"`
}

type workerCheckImage struct {
	Epoch        string                            `json:"epoch"`
	HighWater    uint64                            `json:"high_water"`
	Reservations map[string]workerCheckReservation `json:"reservations"`
	EncodedBytes int64                             `json:"encoded_bytes"`
}

func (i *workerCheckImage) clone() *workerCheckImage {
	if i == nil {
		return nil
	}
	out := *i
	out.Reservations = make(map[string]workerCheckReservation, len(i.Reservations))
	for id, r := range i.Reservations {
		out.Reservations[id] = r
	}
	return &out
}

func workerCheckID(epoch string, sequence uint64) string {
	return fmt.Sprintf("wc.%s.%020d", epoch, sequence)
}

func parseWorkerCheckID(id string) (string, uint64, bool) {
	if len(id) != 60 || !strings.HasPrefix(id, "wc.") || id[39] != '.' {
		return "", 0, false
	}
	epoch := id[3:39]
	u, err := uuid.Parse(epoch)
	if err != nil || u == uuid.Nil || u.String() != epoch {
		return "", 0, false
	}
	sequence, err := strconv.ParseUint(id[40:], 10, 64)
	if err != nil || sequence == 0 || workerCheckID(epoch, sequence) != id {
		return "", 0, false
	}
	return epoch, sequence, true
}

func (i *workerCheckImage) issued(id string) bool {
	epoch, sequence, ok := parseWorkerCheckID(id)
	return i != nil && ok && epoch == i.Epoch && sequence <= i.HighWater
}

func workerCheckReservationCost(id string, r workerCheckReservation) (int64, error) {
	raw, err := json.Marshal(r)
	if err != nil {
		return 0, ErrWorkerExecutionInvalid
	}
	key, _ := json.Marshal(id)
	return int64(len(key) + len(raw) + 2), nil
}

type WorkerCheckAllocateCommand struct {
	ProposedEpoch string `json:"proposed_epoch"`
	OwnerEpoch    string `json:"owner_epoch"`
}

func (c WorkerCheckAllocateCommand) validate(at time.Time) error {
	if _, _, ok := parseWorkerCheckID(workerCheckID(c.ProposedEpoch, 1)); !ok ||
		!catalogIdentifier(c.OwnerEpoch, 256) || at.IsZero() || at.Year() < 1 || at.Year() > 9998 {
		return ErrWorkerExecutionInvalid
	}
	return nil
}

func (f *machine) applyWorkerCheckAllocation(c WorkerCheckAllocateCommand, at time.Time, index uint64) Result {
	if c.OwnerEpoch != f.image.LocalExecutorSession {
		return Result{Err: ErrWorkerSessionExpired}
	}
	state := f.image.WorkerChecks
	if state == nil {
		state = &workerCheckImage{Epoch: c.ProposedEpoch, Reservations: make(map[string]workerCheckReservation), EncodedBytes: workerCheckImageOverhead}
	}
	if state.HighWater == math.MaxUint64 || len(state.Reservations) >= MaxWorkerCheckReservations {
		return Result{Err: ErrWorkerExecutionQuota}
	}
	id := workerCheckID(state.Epoch, state.HighWater+1)
	r := workerCheckReservation{OwnerEpoch: c.OwnerEpoch, CreatedAt: at, CommittedIndex: index}
	cost, err := workerCheckReservationCost(id, r)
	if err != nil || cost > MaxWorkerCheckReservationBytes-state.EncodedBytes {
		return Result{Err: ErrWorkerExecutionQuota}
	}
	state.HighWater++
	state.Reservations[id] = r
	state.EncodedBytes += cost
	f.image.WorkerChecks = state
	f.image.Version = max(f.image.Version, WorkerCheckRetentionFormatVersion)
	return Result{Allowed: true, resultExtensions: resultExtensions{WorkerCheckID: id}}
}

// AllocateWorkerCheckID reserves an identity before encrypting its assignment.
// A lost reply leaves an expiring reservation; it never admits work.
func (s *Store) AllocateWorkerCheckID(ctx context.Context) (string, error) {
	if ctx == nil {
		return "", ErrWorkerExecutionInvalid
	}
	if err := s.controllerHealthContext(ctx); err != nil {
		return "", err
	}
	c := WorkerCheckAllocateCommand{ProposedEpoch: uuid.NewString(), OwnerEpoch: s.executorSession}
	results, err := s.Submit(ctx, []Command{{Kind: "worker_check_allocate", At: time.Now().UTC(), commandExtensions: commandExtensions{WorkerCheckAllocate: &c}}})
	if err != nil {
		return "", errors.Join(ErrCommitUnconfirmed, err)
	}
	if len(results) != 1 {
		return "", errors.Join(ErrCommitUnconfirmed, ErrWorkerExecutionUnavailable)
	}
	if results[0].Err != nil {
		return "", results[0].Err
	}
	if !results[0].Allowed || results[0].WorkerCheckID == "" {
		return "", errors.Join(ErrCommitUnconfirmed, ErrWorkerExecutionUnavailable)
	}
	return results[0].WorkerCheckID, nil
}

func (f *machine) workerCheckExpired(id string) bool {
	state := f.image.WorkerChecks
	if !state.issued(id) {
		return false
	}
	if _, pending := state.Reservations[id]; pending {
		return false // Only committed retirement establishes expiry.
	}
	if f.image.WorkerExecutions != nil {
		if _, retained := f.image.WorkerExecutions.Records[id]; retained {
			return false
		}
	}
	return true
}

func validateWorkerChecks(i image) error {
	state := i.WorkerChecks
	if state == nil {
		return nil
	}
	if i.Version < WorkerCheckRetentionFormatVersion || state.HighWater == 0 ||
		!state.issued(workerCheckID(state.Epoch, state.HighWater)) || state.Reservations == nil || len(state.Reservations) > MaxWorkerCheckReservations {
		return ErrWorkerExecutionInvalid
	}
	used := int64(workerCheckImageOverhead)
	for id, r := range state.Reservations {
		if !state.issued(id) || !catalogIdentifier(r.OwnerEpoch, 256) || r.CreatedAt.IsZero() ||
			r.CreatedAt.Year() < 1 || r.CreatedAt.Year() > 9998 || r.CommittedIndex == 0 || r.CommittedIndex > i.Index {
			return ErrWorkerExecutionInvalid
		}
		if i.WorkerExecutions != nil {
			if _, admitted := i.WorkerExecutions.Records[id]; admitted {
				return ErrWorkerExecutionInvalid
			}
		}
		cost, err := workerCheckReservationCost(id, r)
		if err != nil || cost > MaxWorkerCheckReservationBytes-used {
			return ErrWorkerExecutionQuota
		}
		used += cost
	}
	if state.EncodedBytes != used {
		return ErrWorkerExecutionInvalid
	}
	return nil
}

type WorkerCheckRetireCommand struct {
	OwnerEpoch string   `json:"owner_epoch"`
	IDs        []string `json:"ids"`
}

func (c WorkerCheckRetireCommand) validate(at time.Time) error {
	if !catalogIdentifier(c.OwnerEpoch, 256) || at.IsZero() || at.Year() < 1 || at.Year() > 9998 ||
		len(c.IDs) == 0 || len(c.IDs) > MaxWorkerCheckRetirements {
		return ErrWorkerExecutionInvalid
	}
	for n, id := range c.IDs {
		if _, _, ok := parseWorkerCheckID(id); !ok || n > 0 && id <= c.IDs[n-1] {
			return ErrWorkerExecutionInvalid
		}
	}
	return nil
}

func (f *machine) workerCheckRetirements(at time.Time) map[string]bool {
	due := make(map[string]bool)
	state := f.image.WorkerChecks
	if state == nil {
		return due
	}
	for id, r := range state.Reservations {
		if !at.Before(r.CreatedAt.Add(WorkerCheckReservationLifetime)) {
			due[id] = true
		}
	}
	if f.image.WorkerExecutions == nil {
		return due
	}
	pinned := make(map[string]bool)
	if sessions := f.image.WorkerSessions; sessions != nil {
		for _, session := range sessions.Sessions {
			if session.ReplyExpiredAt == nil && at.Before(session.Response.SessionExpiresAt) {
				for _, offer := range session.Response.Offers {
					pinned[offer.ExecutionID] = true
				}
			}
		}
	}
	type terminal struct {
		id    string
		at    time.Time
		index uint64
	}
	var records []terminal
	for id, r := range f.image.WorkerExecutions.Records {
		if r.Intent.Category != "check" || !state.issued(id) {
			continue
		}
		if receipt, ok := r.TerminalReceipt(); !ok || receipt.Disposition == "unknown" {
			continue
		}
		finished, index := r.terminalPosition()
		records = append(records, terminal{id, finished, index})
	}
	slices.SortFunc(records, func(a, b terminal) int {
		if a.index != b.index {
			if a.index < b.index {
				return -1
			}
			return 1
		}
		return strings.Compare(a.id, b.id)
	})
	for n, r := range records {
		if pinned[r.id] || at.Before(r.at) || n >= len(records)-MaxWorkerCheckReceipts && at.Before(r.at.Add(WorkerCheckReceiptLifetime)) {
			continue
		}
		record := f.image.WorkerExecutions.Records[r.id]
		// Give post-commit measurement its request budget. An interrupted
		// measurement stays missing in the aggregate after record retirement.
		if record.ResultTarget > 0 && record.Result != nil && record.Result.Applied && !record.Result.NoData &&
			record.Observation == nil && at.Before(r.at.Add(10*time.Second)) {
			continue
		}
		due[r.id] = true
	}
	return due
}

func (f *machine) applyWorkerCheckRetirement(c WorkerCheckRetireCommand, at time.Time) Result {
	if c.OwnerEpoch != f.image.LocalExecutorSession {
		return Result{Err: ErrWorkerSessionExpired}
	}
	due := f.workerCheckRetirements(at)
	retiring := make(map[string]bool, len(c.IDs))
	var executionBytes, evidenceBytes, reservationBytes int64
	for _, id := range c.IDs {
		if f.workerCheckExpired(id) {
			continue
		}
		if !due[id] {
			return Result{Err: ErrWorkerExecutionConflict}
		}
		retiring[id] = true
		if r, ok := f.image.WorkerChecks.Reservations[id]; ok {
			cost, err := workerCheckReservationCost(id, r)
			if err != nil {
				return Result{Err: err}
			}
			reservationBytes += cost
		} else {
			r := f.image.WorkerExecutions.Records[id]
			for _, evidence := range r.Evidence {
				cost, err := workerEvidenceRecordCost(evidence)
				if err != nil {
					return Result{Err: err}
				}
				evidenceBytes += cost
			}
			cost, err := workerExecutionRecordCost(id, r)
			if err != nil {
				return Result{Err: err}
			}
			executionBytes += cost
		}
	}
	updates := make(map[string]workerSessionRecord)
	var sessionDelta int64
	if sessions := f.image.WorkerSessions; sessions != nil {
		for uid, old := range sessions.Sessions {
			for _, offer := range old.Response.Offers {
				if !retiring[offer.ExecutionID] {
					continue
				}
				if at.Before(old.Response.SessionExpiresAt) {
					return Result{Err: ErrWorkerSessionConflict}
				}
				copy := old.clone()
				copy.ReplyExpiredAt = &at
				copy.Response.Offers = nil
				oldCost, err := workerSessionRecordCost(uid, old)
				if err != nil {
					return Result{Err: err}
				}
				cost, err := workerSessionRecordCost(uid, copy)
				if err != nil || cost > oldCost {
					return Result{Err: ErrWorkerSessionQuota}
				}
				updates[uid] = copy
				sessionDelta += cost - oldCost
				break
			}
		}
	}
	for id := range retiring {
		if _, reserved := f.image.WorkerChecks.Reservations[id]; reserved {
			delete(f.image.WorkerChecks.Reservations, id)
		} else {
			state := f.image.WorkerExecutions
			if state.ready != nil {
				state.removeIndex(state.Records[id])
			}
			delete(state.Records, id)
		}
	}
	if f.image.WorkerChecks != nil {
		f.image.WorkerChecks.EncodedBytes -= reservationBytes
	}
	if f.image.WorkerExecutions != nil {
		f.image.WorkerExecutions.EncodedBytes -= executionBytes
		f.image.WorkerExecutions.EvidenceBytes -= evidenceBytes
	}
	if sessions := f.image.WorkerSessions; sessions != nil {
		for uid, r := range updates {
			sessions.Sessions[uid] = r
		}
		sessions.EncodedBytes += sessionDelta
	}
	f.image.Version = max(f.image.Version, WorkerCheckRetentionFormatVersion)
	return Result{Allowed: true, resultExtensions: resultExtensions{WorkerChecksRetired: len(retiring)}}
}

// RetireWorkerChecks bounds each maintenance command and rechecks eligibility
// after Raft orders concurrent admissions, results and polling responses.
func (s *Store) RetireWorkerChecks(ctx context.Context, at time.Time, limit int) (int, error) {
	if ctx == nil || at.IsZero() || at.Year() < 1 || at.Year() > 9998 || limit < 1 || limit > MaxWorkerCheckRetirements {
		return 0, ErrWorkerExecutionInvalid
	}
	if s.administrative {
		return 0, ErrWorkerSessionUnavailable
	}
	unlock, err := s.lockControllerState(ctx, false)
	if err != nil {
		return 0, err
	}
	due := s.fsm.workerCheckRetirements(at)
	unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	ids := make([]string, 0, len(due))
	for id := range due {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	if len(ids) == 0 {
		return 0, nil
	}
	c := WorkerCheckRetireCommand{OwnerEpoch: s.executorSession, IDs: ids[:min(limit, len(ids))]}
	results, err := s.Submit(ctx, []Command{{Kind: "worker_check_retire", At: at, commandExtensions: commandExtensions{WorkerCheckRetire: &c}}})
	if err != nil {
		return 0, errors.Join(ErrCommitUnconfirmed, err)
	}
	if len(results) != 1 {
		return 0, errors.Join(ErrCommitUnconfirmed, ErrWorkerExecutionUnavailable)
	}
	if results[0].Err != nil {
		return 0, results[0].Err
	}
	if !results[0].Allowed {
		return 0, ErrWorkerExecutionUnavailable
	}
	return results[0].WorkerChecksRetired, nil
}
