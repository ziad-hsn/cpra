//go:build externaljobs

package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

const WorkerExecutionFormatVersion = 20
const workerExecutionSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-20\n"
const (
	MaxWorkerExecutions         = 4096
	MaxWorkerExecutionBytes     = 256 << 20
	MaxWorkerExecutionPayload   = 128 << 10
	MaxWorkerExecutionsPerType  = 256
	MaxWorkerExecutionPageBytes = 4 << 20
)

var (
	ErrWorkerExecutionInvalid     = errors.New("invalid worker execution intent")
	ErrWorkerExecutionConflict    = errors.New("worker execution identity or admission conflict")
	ErrWorkerExecutionExpired     = errors.New("worker execution deadline elapsed")
	ErrWorkerExecutionQuota       = errors.New("worker execution capacity exhausted")
	ErrWorkerExecutionUnavailable = errors.New("worker execution state unavailable")
)

// WorkerExecutionIntent is controller-owned admission input. Payload contains
// only a sealed, validated assignment; no provider secrets or executable object
// may be supplied in identity fields. Admission does not grant invocation.
type WorkerExecutionIntent struct {
	Notification    *WorkerNotification   `json:"notification,omitempty"`
	Schedule        *WorkerScheduleTicket `json:"schedule,omitempty"`
	ID              string                `json:"id"`
	Revision        string                `json:"revision"`
	MonitorID       string                `json:"monitor_id"`
	MonitorUID      string                `json:"monitor_uid"`
	MonitorRevision string                `json:"monitor_revision"`
	ControlRevision string                `json:"control_revision"`
	Category        string                `json:"category"`
	ActionID        string                `json:"action_id,omitempty"`
	Generation      uint64                `json:"generation,omitempty"`
	Source          CatalogKey            `json:"source"`
	SourceUID       string                `json:"source_uid"`
	SourceRevision  string                `json:"source_revision"`
	JobType         JobTypeReference      `json:"job_type"`
	Guard           CatalogGuard          `json:"guard"`
	Scheduled       time.Time             `json:"scheduled"`
	Deadline        time.Time             `json:"deadline"`
	Payload         secureconfig.Envelope `json:"payload"`
}

func (i WorkerExecutionIntent) Clone() WorkerExecutionIntent {
	i.Notification = i.Notification.clone()
	if i.Schedule != nil {
		copy := *i.Schedule
		i.Schedule = &copy
	}
	i.Guard = i.Guard.Clone()
	i.Payload = i.Payload.Clone()
	return i
}
func (i WorkerExecutionIntent) Binding(storeID string) secureconfig.Binding {
	return secureconfig.Binding{StoreID: storeID, Kind: "WorkerExecution", ID: i.ID, UID: i.MonitorUID, Revision: i.Revision, Purpose: "worker-assignment-v1"}
}

// Validate checks structural identity and encrypted payload bounds, not live admission.
func (i WorkerExecutionIntent) Validate() error { return i.validate() }

func (i WorkerExecutionIntent) validate() error {
	if !i.Notification.valid() || i.Notification != nil && i.Category != "notification" {
		return ErrWorkerExecutionInvalid
	}
	if i.Schedule != nil && (i.Category != "check" || !i.Schedule.valid(i.Scheduled)) {
		return ErrWorkerExecutionInvalid
	}
	for _, id := range []string{i.ID, i.Revision, i.MonitorID, i.MonitorUID, i.MonitorRevision, i.ControlRevision, i.SourceUID, i.SourceRevision} {
		if !catalogIdentifier(id, 256) {
			return ErrWorkerExecutionInvalid
		}
	}
	if i.Category != "check" && i.Category != "recovery" && i.Category != "notification" || i.JobType.Validate() != nil || i.JobType.Category != i.Category || i.Guard.validate(i.MonitorID) != nil || i.Guard.Removed || i.Guard.monitorUID(i.MonitorID) != i.MonitorUID || i.Source.validate() != nil || i.Payload.Validate() != nil || len(i.Payload.Ciphertext) > MaxWorkerExecutionPayload+16 || i.Scheduled.IsZero() || i.Scheduled.Year() < 1 || i.Scheduled.Year() > 9998 || !i.Deadline.After(i.Scheduled) || i.Deadline.Year() > 9999 {
		return ErrWorkerExecutionInvalid
	}
	if i.Category == "check" {
		if i.Generation == 0 || i.ActionID != "" {
			return ErrWorkerExecutionInvalid
		}
	} else if i.Generation != 0 || !catalogIdentifier(i.ActionID, 256) {
		return ErrWorkerExecutionInvalid
	}
	if i.Category == "notification" {
		if i.Source.Kind != "NotificationEndpoint" && (i.Source != (CatalogKey{Kind: "Monitor", ID: i.MonitorID}) || i.SourceUID != i.MonitorUID) {
			return ErrWorkerExecutionInvalid
		}
	} else if i.Source != (CatalogKey{Kind: "Monitor", ID: i.MonitorID}) || i.SourceUID != i.MonitorUID {
		return ErrWorkerExecutionInvalid
	}
	for _, condition := range i.Guard.Conditions {
		if condition.Key == i.Source && condition.UID == i.SourceUID && condition.Revision == i.SourceRevision {
			return nil
		}
	}
	return ErrWorkerExecutionInvalid
}

func (i WorkerExecutionIntent) minimumFormat() int {
	if i.Notification != nil {
		return 45
	}
	if i.Schedule != nil {
		return WorkerScheduleFormatVersion
	}
	if i.Category == "notification" && i.Source.Kind == "Monitor" {
		return WorkerNotificationFormatVersion
	}
	return WorkerExecutionFormatVersion
}

type WorkerExecutionRecord struct {
	RetiredOffers  []WorkerExecutionLifecycle `json:"retired_offers,omitempty"`
	ResultTarget   time.Duration              `json:"result_target,omitempty"`
	SLOEpoch       uint64                     `json:"slo_epoch,omitempty"`
	Observation    *WorkerResultObservation   `json:"observation,omitempty"`
	Intent         WorkerExecutionIntent      `json:"intent"`
	OwnerEpoch     string                     `json:"owner_epoch"`
	CreatedAt      time.Time                  `json:"created_at"`
	CommittedIndex uint64                     `json:"committed_index"`
	Digest         string                     `json:"digest"`
	Lifecycle      *WorkerExecutionLifecycle  `json:"lifecycle,omitempty"`
	Result         *WorkerResultRecord        `json:"result,omitempty"`
	Evidence       []WorkerEvidenceRecord     `json:"evidence,omitempty"`
	Finalization   *WorkerFinalization        `json:"finalization,omitempty"`
}

func (r WorkerExecutionRecord) Clone() WorkerExecutionRecord {
	r.RetiredOffers = slices.Clone(r.RetiredOffers)
	if r.Observation != nil {
		copy := *r.Observation
		r.Observation = &copy
	}
	if r.Finalization != nil {
		value := *r.Finalization
		r.Finalization = &value
	}
	r.Intent = r.Intent.Clone()
	if r.Lifecycle != nil {
		value := *r.Lifecycle
		if value.Heartbeat != nil {
			heartbeat := *value.Heartbeat
			value.Heartbeat = &heartbeat
		}
		r.Lifecycle = &value
	}
	if r.Result != nil {
		value := r.Result.Clone()
		r.Result = &value
	}
	if r.Evidence != nil {
		values := make([]WorkerEvidenceRecord, len(r.Evidence))
		for n, value := range r.Evidence {
			values[n] = value.Clone()
		}
		r.Evidence = values
	}
	return r
}

// WorkerExecutionCommand is stamped by CommitWorkerExecution before admission.
// Apply generates neither time nor identity and performs no external I/O.
type WorkerExecutionCommand struct {
	CheckReserved     bool                  `json:"check_reserved,omitempty"`
	SchedulingVersion int                   `json:"scheduling_version,omitempty"`
	ResultTarget      time.Duration         `json:"result_target,omitempty"`
	Intent            WorkerExecutionIntent `json:"intent"`
	OwnerEpoch        string                `json:"owner_epoch"`
}

func (c WorkerExecutionCommand) validate(at time.Time) error {
	if c.Intent.Schedule != nil && (c.Intent.Schedule.OwnerEpoch != c.OwnerEpoch || c.ResultTarget <= 0) {
		return ErrWorkerExecutionInvalid
	}
	_, _, numbered := parseWorkerCheckID(c.Intent.ID)
	if c.CheckReserved && (!numbered || c.Intent.Category != "check" || c.SchedulingVersion != 1) || !c.CheckReserved && strings.HasPrefix(c.Intent.ID, "wc.") {
		return ErrWorkerExecutionInvalid
	}
	if c.SchedulingVersion < 0 || c.SchedulingVersion > 1 || c.ResultTarget < 0 || c.ResultTarget > 0 && c.Intent.Category != "check" || c.Intent.validate() != nil || !catalogIdentifier(c.OwnerEpoch, 256) || at.IsZero() || at.Year() < 1 || at.Year() > 9998 {
		return ErrWorkerExecutionInvalid
	}
	return nil
}

// This frozen projection preserves retained identities when execution state grows.
// Changes to these fields or their wire representations require a new projection.
type workerExecutionIntentDigestV1 struct {
	ID              string                `json:"id"`
	Revision        string                `json:"revision"`
	MonitorID       string                `json:"monitor_id"`
	MonitorUID      string                `json:"monitor_uid"`
	MonitorRevision string                `json:"monitor_revision"`
	ControlRevision string                `json:"control_revision"`
	Category        string                `json:"category"`
	ActionID        string                `json:"action_id,omitempty"`
	Generation      uint64                `json:"generation,omitempty"`
	Source          CatalogKey            `json:"source"`
	SourceUID       string                `json:"source_uid"`
	SourceRevision  string                `json:"source_revision"`
	JobType         JobTypeReference      `json:"job_type"`
	Guard           CatalogGuard          `json:"guard"`
	Scheduled       time.Time             `json:"scheduled"`
	Deadline        time.Time             `json:"deadline"`
	Payload         secureconfig.Envelope `json:"payload"`
}

func workerExecutionDigest(i WorkerExecutionIntent) string {
	projection := workerExecutionIntentDigestV1{
		ID: i.ID, Revision: i.Revision, MonitorID: i.MonitorID, MonitorUID: i.MonitorUID,
		MonitorRevision: i.MonitorRevision, ControlRevision: i.ControlRevision, Category: i.Category,
		ActionID: i.ActionID, Generation: i.Generation, Source: i.Source, SourceUID: i.SourceUID,
		SourceRevision: i.SourceRevision, JobType: i.JobType, Guard: i.Guard,
		Scheduled: i.Scheduled, Deadline: i.Deadline, Payload: i.Payload,
	}
	raw, _ := json.Marshal(projection)
	if i.Notification != nil {
		value, _ := json.Marshal(struct {
			Intent       workerExecutionIntentDigestV1 `json:"intent"`
			Notification *WorkerNotification           `json:"notification"`
		}{projection, i.Notification})
		return identity("cpra/worker/execution-intent/v3\x00" + string(value))
	}
	if i.Schedule != nil {
		value, _ := json.Marshal(struct {
			Intent   workerExecutionIntentDigestV1 `json:"intent"`
			Schedule workerScheduleTicketDigestV2  `json:"schedule"`
		}{projection, i.Schedule.digestProjection()})
		return identity("cpra/worker/execution-intent/v2\x00" + string(value))
	}
	return identity("cpra/worker/execution-intent/v1\x00" + string(raw))
}

func (s *Store) CommitWorkerExecution(ctx context.Context, intent WorkerExecutionIntent) (WorkerExecutionRecord, error) {
	if ctx == nil {
		return WorkerExecutionRecord{}, ErrWorkerExecutionInvalid
	}
	if err := ctx.Err(); err != nil {
		return WorkerExecutionRecord{}, err
	}
	if err := intent.validate(); err != nil {
		return WorkerExecutionRecord{}, err
	}
	if err := s.controllerHealthContext(ctx); err != nil {
		return WorkerExecutionRecord{}, err
	}
	command := WorkerExecutionCommand{SchedulingVersion: 1, Intent: intent.Clone(), OwnerEpoch: s.executorSession}
	_, _, command.CheckReserved = parseWorkerCheckID(intent.ID)
	if intent.Category == "check" {
		command.ResultTarget = s.config.SLO.ResultTarget
	}
	results, err := s.Submit(ctx, []Command{{Kind: "worker_execution", At: time.Now().UTC(), commandExtensions: commandExtensions{WorkerExecution: &command}}})
	if err != nil {
		return WorkerExecutionRecord{}, errors.Join(ErrCommitUnconfirmed, err)
	}
	if len(results) != 1 {
		return WorkerExecutionRecord{}, errors.Join(ErrCommitUnconfirmed, ErrWorkerExecutionUnavailable)
	}
	if results[0].Err != nil {
		return WorkerExecutionRecord{}, results[0].Err
	}
	if !results[0].Allowed || results[0].WorkerExecution == nil {
		return WorkerExecutionRecord{}, errors.Join(ErrCommitUnconfirmed, ErrWorkerExecutionUnavailable)
	}
	return results[0].WorkerExecution.Clone(), nil
}

// WorkerExecution returns a detached encrypted record, including an old owner or
// expired intent. It grants no permission; callers must not treat lookup as Ready.
func (s *Store) WorkerExecution(ctx context.Context, id string) (WorkerExecutionRecord, bool, error) {
	if !catalogIdentifier(id, 256) {
		return WorkerExecutionRecord{}, false, ErrWorkerExecutionInvalid
	}
	unlock, err := s.lockCatalogReadState(ctx, false)
	if err != nil {
		return WorkerExecutionRecord{}, false, err
	}
	defer unlock()
	if s.fsm.image.WorkerExecutions == nil {
		return WorkerExecutionRecord{}, false, nil
	}
	record, ok := s.fsm.image.WorkerExecutions.Records[id]
	if err := ctx.Err(); err != nil {
		return WorkerExecutionRecord{}, false, err
	}
	return record.Clone(), ok, nil
}

type WorkerExecutionPage struct {
	Items []WorkerExecutionRecord
	Next  string
	More  bool
}

// WorkerExecutionsReady visits at most 256 entries of one exact pinned type.
// Only the current owner and unexpired, still eligible intents are returned.
// This is an encrypted dispatch inventory, not a stable public API cursor.
func (s *Store) WorkerExecutionsReady(ctx context.Context, kind JobTypeReference, afterID string, limit int) (WorkerExecutionPage, error) {
	var page WorkerExecutionPage
	if kind.Validate() != nil || afterID != "" && !catalogIdentifier(afterID, 256) || limit < 1 || limit > 256 {
		return page, ErrWorkerExecutionInvalid
	}
	unlock, err := s.lockControllerState(ctx, true)
	if err != nil {
		return page, err
	}
	defer unlock()
	f := s.fsm
	state := f.image.WorkerExecutions
	if state == nil {
		return page, nil
	}
	if err := state.ensureIndexes(ctx); err != nil {
		return page, err
	}
	tree := state.ready[jobTypeReferenceKey(kind)]
	if tree == nil {
		return page, nil
	}
	var used int64
	tree.AscendGreaterOrEqual(afterID, func(id string) bool {
		if id == afterID {
			return true
		}
		if err = ctx.Err(); err != nil {
			return false
		}
		record := state.Records[id]
		if record.Lifecycle != nil || record.OwnerEpoch != s.executorSession || f.checkWorkerExecution(record.Intent, time.Now().UTC()) != nil {
			return true
		}
		cost, e := workerExecutionRecordCost(id, record)
		if e != nil {
			err = e
			return false
		}
		if len(page.Items) == limit || cost > MaxWorkerExecutionPageBytes-used {
			page.More = true
			return false
		}
		used += cost
		page.Items = append(page.Items, record.Clone())
		page.Next = id
		return true
	})
	if f.err != nil {
		return WorkerExecutionPage{}, errControllerUnavailable
	}
	if err != nil {
		return WorkerExecutionPage{}, err
	}
	if err = ctx.Err(); err != nil {
		return WorkerExecutionPage{}, err
	}
	return page, nil
}
