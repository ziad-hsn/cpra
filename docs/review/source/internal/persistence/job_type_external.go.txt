//go:build externaljobs

package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"time"
	"unicode"
)

// JobTypeFormatVersion adds encrypted immutable JobType versions, not worker
// enrollment, execution permission, or an external runtime adapter.
const JobTypeFormatVersion = 15
const AgentWorkerFormatVersion = 32
const RecoveryNotificationWorkerFormatVersion = 34
const AgentObservationWorkerFormatVersion = 36
const NotificationWorkerFormatVersion = 39
const DeliveryWorkerFormatVersion = 41
const LatestFormatVersion = DeliveryWorkerFormatVersion
const deliveryWorkerSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-41\n"
const notificationWorkerSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-39\n"
const agentObservationWorkerSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-36\n"
const recoveryNotificationWorkerSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-34\n"
const agentWorkerSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-32\n"
const jobTypeSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-15\n"

// Format 31 is readable without externaljobs. Combining it with any optional
// state uses a distinct format so a default binary cannot discard worker data.
func joinStorageFormat(a, b int) int {
	if max(a, b) == DeliveryFormatVersion && (externalStorageFormat(a) || externalStorageFormat(b)) {
		return DeliveryWorkerFormatVersion
	}
	if max(a, b) == NotificationFormatVersion && (externalStorageFormat(a) || externalStorageFormat(b)) {
		return NotificationWorkerFormatVersion
	}
	if max(a, b) == AgentObservationFormatVersion && (externalStorageFormat(a) || externalStorageFormat(b)) {
		return AgentObservationWorkerFormatVersion
	}
	if max(a, b) == RecoveryNotificationFormatVersion && (externalStorageFormat(a) || externalStorageFormat(b)) {
		return RecoveryNotificationWorkerFormatVersion
	}
	if max(a, b) == AgentActionFormatVersion && (externalStorageFormat(a) || externalStorageFormat(b)) {
		return AgentWorkerFormatVersion
	}
	return max(a, b)
}

const (
	MaxJobTypes          = 1024
	MaxJobTypeVersions   = 64
	MaxJobTypeStateBytes = 8 << 20
	MaxJobTypeTotalBytes = 64 << 20
	MaxJobTypePageBytes  = 4 << 20
)

var (
	ErrJobTypeInvalid         = errors.New("invalid durable JobType")
	ErrJobTypeConflict        = errors.New("JobType incarnation or revision conflict")
	ErrJobTypeVersionConflict = errors.New("JobType version is immutable")
	ErrJobTypeQuota           = errors.New("durable JobType quota exceeded")
	ErrJobTypeUnavailable     = errors.New("durable JobType unavailable")
)

type commandExtensions struct {
	WorkerSchedule      *WorkerScheduleCommand      `json:"worker_schedule,omitempty"`
	WorkerExpire        *WorkerExpireCommand        `json:"worker_expire,omitempty"`
	WorkerCheckAllocate *WorkerCheckAllocateCommand `json:"worker_check_allocate,omitempty"`
	WorkerCheckRetire   *WorkerCheckRetireCommand   `json:"worker_check_retire,omitempty"`
	WorkerRequeue       *WorkerRequeueCommand       `json:"worker_requeue,omitempty"`
	WorkerObservation   *WorkerObservationCommand   `json:"worker_observation,omitempty"`
	WorkerFinalize      *WorkerFinalizeCommand      `json:"worker_finalize,omitempty"`
	WorkerEvidence      *WorkerEvidenceCommand      `json:"worker_evidence,omitempty"`
	WorkerHeartbeat     *WorkerHeartbeatCommand     `json:"worker_heartbeat,omitempty"`
	WorkerResult        *WorkerResultCommand        `json:"worker_result,omitempty"`
	WorkerStart         *WorkerStartCommand         `json:"worker_start,omitempty"`
	WorkerExecution     *WorkerExecutionCommand     `json:"worker_execution,omitempty"`
	WorkerSession       *WorkerSessionCommand       `json:"worker_session,omitempty"`
	WorkerPolicy        *WorkerPolicyCommand        `json:"worker_policy,omitempty"`
	JobType             *JobTypeCommand             `json:"job_type,omitempty"`
	JobTypeAllocation   *JobTypeOperationAllocation `json:"job_type_allocation,omitempty"`
}
type imageExtensions struct {
	WorkerChecks     *workerCheckImage     `json:"worker_checks,omitempty"`
	WorkerSLO        *workerSLOImage       `json:"worker_slo,omitempty"`
	WorkerExecutions *workerExecutionImage `json:"worker_executions,omitempty"`
	WorkerSessions   *workerSessionImage   `json:"worker_sessions,omitempty"`
	WorkerPolicy     *WorkerPolicyState    `json:"worker_policy,omitempty"`
	JobTypes         *jobTypeImage         `json:"job_types,omitempty"`
}
type resultExtensions struct {
	WorkerCheckID       string
	WorkerChecksRetired int
	WorkerEvidence      *WorkerReceipt
	WorkerHeartbeat     *WorkerHeartbeatResponse
	WorkerResult        *WorkerReceipt
	WorkerExecution     *WorkerExecutionRecord
	WorkerStart         *WorkerStartResponse
	WorkerPoll          *WorkerPollResponse
	JobType             *JobTypeState
	WorkerPolicy        *WorkerPolicyState
}

// JobTypeVersion contains only encrypted resource bytes and non-secret exact
// selectors. Parameters, schemas, display metadata and provider data stay in
// Record.Payload. Executions must eventually read Versions, never Current.
type JobTypeVersion struct {
	Record          CatalogRecord `json:"record"`
	Version         string        `json:"version"`
	Category        string        `json:"category"`
	Handler         string        `json:"handler"`
	ProtocolVersion string        `json:"protocol_version"`
	SchemaProfile   string        `json:"schema_profile"`
}

func (v JobTypeVersion) Clone() JobTypeVersion { v.Record = v.Record.Clone(); return v }

// JobTypeState retains all admitted version names, including across deletion
// and recreation. Current may have newer display metadata for the same version.
// No version or tombstone is evicted to make room for another descriptor.
type JobTypeState struct {
	Current       JobTypeVersion            `json:"current"`
	Versions      map[string]JobTypeVersion `json:"versions"`
	CommandDigest string                    `json:"command_digest"`
}

func (s JobTypeState) Clone() JobTypeState {
	s.Current = s.Current.Clone()
	versions := make(map[string]JobTypeVersion, len(s.Versions))
	for version, value := range s.Versions {
		versions[version] = value.Clone()
	}
	s.Versions = versions
	return s
}

// JobTypeCommand is prepared by the trusted management boundary. A same-current-
// version replacement requires RetainedVersionRevision after preparation has
// decrypted that original version and proved full spec equality. This fence is
// never a public caller's assertion. Apply preserves its original ciphertext.
type JobTypeCommand struct {
	OperationID             string            `json:"operation_id,omitempty"`
	Action                  string            `json:"action"`
	Value                   JobTypeVersion    `json:"value"`
	ExpectedUID             string            `json:"expected_uid,omitempty"`
	ExpectedRevision        string            `json:"expected_revision,omitempty"`
	RetainedVersionRevision string            `json:"retained_version_revision,omitempty"`
	Authority               OperatorAuthority `json:"authority"`
}

type jobTypeImage struct {
	Records      map[string]JobTypeState `json:"records"`
	EncodedBytes int64                   `json:"encoded_bytes"`
}

func jobTypeSelector(s string) bool {
	if !catalogIdentifier(s, 256) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func (v JobTypeVersion) validate() error {
	r := v.Record
	if r.validate() != nil || r.Key.Kind != "JobType" || r.Purpose != "job-type" ||
		len(r.References) != 0 || r.DependentsVersion != 0 || r.Generation > math.MaxInt64 ||
		!jobTypeSelector(v.Version) || !jobTypeSelector(v.Handler) || v.ProtocolVersion != "1" || v.SchemaProfile != "cpra.schema.v1" ||
		r.CreatedAt.Year() < 1 || r.CreatedAt.Year() > 9999 || r.UpdatedAt.Year() < 1 || r.UpdatedAt.Year() > 9999 {
		return ErrJobTypeInvalid
	}
	if v.Category != "check" && v.Category != "recovery" && v.Category != "notification" {
		return ErrJobTypeInvalid
	}
	return nil
}

func (c JobTypeCommand) validate(at time.Time) error {
	if c.OperationID != "" {
		if _, _, err := ParseOperationHandle(c.OperationID); err != nil || at.Before(c.Value.Record.UpdatedAt) {
			return ErrJobTypeInvalid
		}
		c.OperationID = ""
		return c.validate(c.Value.Record.UpdatedAt)
	}
	if c.Value.validate() != nil || c.Authority.validate() != nil || !c.Value.Record.UpdatedAt.Equal(at) || c.Value.Record.CommittedIndex != 0 {
		return ErrJobTypeInvalid
	}
	switch c.Action {
	case "create":
		if c.Value.Record.Removed || c.ExpectedUID != "" || c.ExpectedRevision != "" || c.RetainedVersionRevision != "" || c.Value.Record.Generation != 1 || !c.Value.Record.CreatedAt.Equal(at) {
			return ErrJobTypeInvalid
		}
	case "replace", "delete":
		if !catalogIdentifier(c.ExpectedUID, 256) || !catalogIdentifier(c.ExpectedRevision, 256) || c.Value.Record.UID != c.ExpectedUID || c.Value.Record.Revision == c.ExpectedRevision || (c.Action == "delete") != c.Value.Record.Removed {
			return ErrJobTypeInvalid
		}
		if c.RetainedVersionRevision != "" && (c.Action != "replace" || !catalogIdentifier(c.RetainedVersionRevision, 256)) {
			return ErrJobTypeInvalid
		}
	default:
		return ErrJobTypeInvalid
	}
	return nil
}

func validateCommandExtensions(c Command) (bool, error) {
	if c.Kind == "worker_schedule" {
		extra := c
		extra.Kind, extra.At, extra.WorkerSchedule = "", time.Time{}, nil
		if extra != (Command{}) || c.WorkerSchedule == nil {
			return true, ErrWorkerExecutionInvalid
		}
		return true, c.WorkerSchedule.validate(c.At)
	}
	if c.WorkerSchedule != nil {
		return true, ErrWorkerExecutionInvalid
	}
	if c.Kind == "worker_expire" {
		extra := c
		extra.Kind, extra.At, extra.WorkerExpire = "", time.Time{}, nil
		if extra != (Command{}) || c.WorkerExpire == nil {
			return true, ErrWorkerExecutionInvalid
		}
		return true, c.WorkerExpire.validate(c.At)
	}
	if c.WorkerExpire != nil {
		return true, ErrWorkerExecutionInvalid
	}
	if c.Kind == "worker_check_allocate" {
		extra := c
		extra.Kind, extra.At, extra.WorkerCheckAllocate = "", time.Time{}, nil
		if extra != (Command{}) || c.WorkerCheckAllocate == nil {
			return true, ErrWorkerExecutionInvalid
		}
		return true, c.WorkerCheckAllocate.validate(c.At)
	}
	if c.WorkerCheckAllocate != nil {
		return true, ErrWorkerExecutionInvalid
	}
	if c.Kind == "worker_check_retire" {
		extra := c
		extra.Kind, extra.At, extra.WorkerCheckRetire = "", time.Time{}, nil
		if extra != (Command{}) || c.WorkerCheckRetire == nil {
			return true, ErrWorkerExecutionInvalid
		}
		return true, c.WorkerCheckRetire.validate(c.At)
	}
	if c.WorkerCheckRetire != nil {
		return true, ErrWorkerExecutionInvalid
	}
	if c.Kind == "worker_requeue" {
		extra := c
		extra.Kind, extra.At, extra.WorkerRequeue = "", time.Time{}, nil
		if extra != (Command{}) || c.WorkerRequeue == nil {
			return true, ErrWorkerExecutionInvalid
		}
		return true, c.WorkerRequeue.validate(c.At)
	}
	if c.WorkerRequeue != nil {
		return true, ErrWorkerExecutionInvalid
	}

	if c.Kind == "worker_observation" {
		extra := c
		extra.Kind, extra.At, extra.WorkerObservation = "", time.Time{}, nil
		if extra != (Command{}) || c.WorkerObservation == nil {
			return true, ErrWorkerExecutionInvalid
		}
		return true, c.WorkerObservation.validate(c.At)
	}
	if c.WorkerObservation != nil {
		return true, ErrWorkerExecutionInvalid
	}
	if c.Kind == "worker_finalize" {
		extra := c
		extra.Kind, extra.At, extra.WorkerFinalize = "", time.Time{}, nil
		if extra != (Command{}) || c.WorkerFinalize == nil {
			return true, ErrWorkerExecutionInvalid
		}
		return true, c.WorkerFinalize.validate(c.At)
	}
	if c.WorkerFinalize != nil {
		return true, ErrWorkerExecutionInvalid
	}
	if c.Kind == "worker_evidence" {
		extra := c
		extra.Kind, extra.At, extra.WorkerEvidence = "", time.Time{}, nil
		if extra != (Command{}) || c.WorkerEvidence == nil {
			return true, ErrWorkerExecutionInvalid
		}
		return true, c.WorkerEvidence.validate(c.At)
	}
	if c.WorkerEvidence != nil {
		return true, ErrWorkerExecutionInvalid
	}
	if c.Kind == "worker_heartbeat" {
		extra := c
		extra.Kind, extra.At, extra.WorkerHeartbeat = "", time.Time{}, nil
		if extra != (Command{}) || c.WorkerHeartbeat == nil {
			return true, ErrWorkerExecutionInvalid
		}
		return true, c.WorkerHeartbeat.validate(c.At)
	}
	if c.WorkerHeartbeat != nil {
		return true, ErrWorkerExecutionInvalid
	}
	if c.Kind == "worker_result" {
		extra := c
		extra.Kind, extra.At, extra.WorkerResult = "", time.Time{}, nil
		if extra != (Command{}) || c.WorkerResult == nil {
			return true, ErrWorkerExecutionInvalid
		}
		return true, c.WorkerResult.validate(c.At)
	}
	if c.WorkerResult != nil {
		return true, ErrWorkerExecutionInvalid
	}
	if c.Kind == "worker_start" {
		extra := c
		extra.Kind, extra.At, extra.WorkerStart = "", time.Time{}, nil
		if extra != (Command{}) || c.WorkerStart == nil {
			return true, ErrWorkerExecutionInvalid
		}
		return true, c.WorkerStart.validate(c.At)
	}
	if c.WorkerStart != nil {
		return true, ErrWorkerExecutionInvalid
	}

	if c.Config != nil && policyMinimumFormat(c.Config.Policy) != FormatVersion && c.Kind != "configure" {
		return true, ErrWorkerExecutionInvalid
	}
	if c.Kind == "worker_execution" {
		extra := c
		extra.Kind, extra.At, extra.WorkerExecution = "", time.Time{}, nil
		if extra != (Command{}) || c.WorkerExecution == nil {
			return true, ErrWorkerExecutionInvalid
		}
		return true, c.WorkerExecution.validate(c.At)
	}
	if c.WorkerExecution != nil {
		return true, ErrWorkerExecutionInvalid
	}

	if c.Kind == "worker_poll" {
		extra := c
		extra.Kind, extra.At, extra.WorkerSession = "", time.Time{}, nil
		if extra != (Command{}) || c.WorkerSession == nil {
			return true, ErrWorkerSessionInvalid
		}
		return true, c.WorkerSession.validate(c.At)
	}
	if c.WorkerSession != nil {
		return true, ErrWorkerSessionInvalid
	}
	if c.Kind == "worker_policy" {
		extra := c
		extra.Kind, extra.At, extra.WorkerPolicy = "", time.Time{}, nil
		if extra != (Command{}) || c.WorkerPolicy == nil || !c.At.Equal(c.WorkerPolicy.At) {
			return true, ErrWorkerPolicyInvalid
		}
		return true, c.WorkerPolicy.Validate()
	}
	if c.WorkerPolicy != nil {
		return true, ErrWorkerPolicyInvalid
	}
	if c.Kind == "job_type_reserve" {
		extra := c
		extra.Kind, extra.At, extra.JobTypeAllocation = "", time.Time{}, nil
		if extra != (Command{}) || c.JobTypeAllocation == nil {
			return true, ErrOperationReservation
		}
		return true, c.JobTypeAllocation.validate(c.At)
	}
	if c.JobTypeAllocation != nil {
		return true, ErrOperationReservation
	}
	if c.Kind != "job_type" {
		if c.JobType != nil {
			return true, ErrJobTypeInvalid
		}
		return false, nil
	}
	if c.JobType == nil {
		return true, ErrJobTypeInvalid
	}
	extra := c
	extra.Kind, extra.At, extra.JobType = "", time.Time{}, nil
	if extra != (Command{}) {
		return true, ErrJobTypeInvalid
	}
	return true, c.JobType.validate(c.At)
}
func hasCommandExtensions(c Command) bool {
	return c.WorkerSchedule != nil || c.WorkerExpire != nil || c.WorkerCheckAllocate != nil || c.WorkerCheckRetire != nil || c.WorkerRequeue != nil || c.WorkerObservation != nil || c.WorkerFinalize != nil || c.WorkerEvidence != nil || c.WorkerHeartbeat != nil || c.WorkerResult != nil || c.WorkerStart != nil || c.WorkerExecution != nil || c.JobType != nil || c.JobTypeAllocation != nil || c.WorkerPolicy != nil || c.WorkerSession != nil
}
func commandExtensionMinimumFormat(c Command) int {
	if c.WorkerSession != nil && c.WorkerSession.SelectionVersion != 0 {
		return WorkerQueueFormatVersion
	}
	if c.WorkerSchedule != nil || c.Kind == "worker_schedule" || c.WorkerExecution != nil && c.WorkerExecution.Intent.Schedule != nil {
		return WorkerScheduleFormatVersion
	}
	if c.Kind == "worker_expire" || c.WorkerExpire != nil {
		return WorkerExpiryFormatVersion
	}
	if c.WorkerExecution != nil && c.WorkerExecution.Intent.minimumFormat() == WorkerNotificationFormatVersion || c.Config != nil && policyMinimumFormat(c.Config.Policy) == WorkerNotificationFormatVersion {
		return WorkerNotificationFormatVersion
	}
	if c.WorkerPolicy != nil {
		for _, grant := range c.WorkerPolicy.Worker.Grants {
			if grant.minimumFormat() == WorkerNotificationFormatVersion {
				return WorkerNotificationFormatVersion
			}
		}
	}
	if c.Kind == "worker_check_allocate" || c.WorkerCheckAllocate != nil || c.Kind == "worker_check_retire" || c.WorkerCheckRetire != nil || c.WorkerExecution != nil && c.WorkerExecution.CheckReserved {
		return WorkerCheckRetentionFormatVersion
	}
	if c.Kind == "worker_requeue" || c.WorkerRequeue != nil || c.WorkerExecution != nil && c.WorkerExecution.SchedulingVersion != 0 {
		return WorkerSchedulingFormatVersion
	}
	if c.Kind == "worker_observation" || c.WorkerObservation != nil || c.WorkerExecution != nil && c.WorkerExecution.ResultTarget != 0 {
		return WorkerSLOFormatVersion
	}
	if c.Kind == "worker_finalize" || c.WorkerFinalize != nil {
		return WorkerFinalizationFormatVersion
	}
	if c.Kind == "worker_heartbeat" || c.WorkerHeartbeat != nil || c.Kind == "worker_evidence" || c.WorkerEvidence != nil {
		return WorkerLifecycleFormatVersion
	}
	if c.Kind == "worker_result" || c.WorkerResult != nil {
		return WorkerResultFormatVersion
	}
	if c.Kind == "worker_start" || c.WorkerStart != nil || c.WorkerSession != nil && len(c.WorkerSession.ProposedLeaseIDs) > 0 {
		return WorkerOfferFormatVersion
	}
	if c.Kind == "worker_execution" || c.WorkerExecution != nil {
		return WorkerExecutionFormatVersion
	}
	if c.Config != nil && policyMinimumFormat(c.Config.Policy) > FormatVersion {
		return WorkerExecutionFormatVersion
	}

	if c.Kind == "worker_poll" || c.WorkerSession != nil {
		return WorkerSessionFormatVersion
	}
	if version := catalogReferenceCommandFormat(c); version != FormatVersion {
		return version
	}
	if c.Kind == "worker_policy" || c.WorkerPolicy != nil {
		return WorkerPolicyFormatVersion
	}
	if c.Kind == "job_type_reserve" || c.JobTypeAllocation != nil || c.JobType != nil && c.JobType.OperationID != "" {
		return JobTypeOperationFormatVersion
	}
	if c.Kind == "job_type" || c.JobType != nil {
		return JobTypeFormatVersion
	}
	return FormatVersion
}
func externalStorageFormat(version int) bool {
	if version == DeliveryWorkerFormatVersion {
		return true
	}
	if version == NotificationWorkerFormatVersion {
		return true
	}
	if version == WorkerQueueFormatVersion {
		return true
	}
	if version == AgentWorkerFormatVersion || version == RecoveryNotificationWorkerFormatVersion || version == AgentObservationWorkerFormatVersion {
		return true
	}
	return version == WorkerScheduleFormatVersion || version == WorkerExpiryFormatVersion || version == WorkerNotificationFormatVersion || version == WorkerCheckRetentionFormatVersion || version == WorkerSchedulingFormatVersion || version == WorkerSLOFormatVersion || version == WorkerFinalizationFormatVersion || version == WorkerLifecycleFormatVersion || version == WorkerResultFormatVersion || version == WorkerOfferFormatVersion || version == WorkerExecutionFormatVersion || version == JobTypeFormatVersion || version == JobTypeOperationFormatVersion || version == WorkerPolicyFormatVersion || version == CatalogJobTypeFormatVersion || version == WorkerSessionFormatVersion
}
func commandExtensionWriteFormat(c Command) int {
	version := commandExtensionMinimumFormat(c)
	if c.Kind == "worker_result" || c.Kind == "worker_start" {
		return joinStorageFormat(version, DeliveryFormatVersion)
	}
	return version
}
func externalSnapshotMagic(version int) string {
	if version == DeliveryWorkerFormatVersion {
		return deliveryWorkerSnapshotMagic
	}
	if version == NotificationWorkerFormatVersion {
		return notificationWorkerSnapshotMagic
	}
	if version == WorkerQueueFormatVersion {
		return workerQueueSnapshotMagic
	}
	if version == AgentObservationWorkerFormatVersion {
		return agentObservationWorkerSnapshotMagic
	}
	if version == RecoveryNotificationWorkerFormatVersion {
		return recoveryNotificationWorkerSnapshotMagic
	}
	if version == AgentWorkerFormatVersion {
		return agentWorkerSnapshotMagic
	}
	if version == WorkerScheduleFormatVersion {
		return workerScheduleSnapshotMagic
	}
	if version == WorkerExpiryFormatVersion {
		return workerExpirySnapshotMagic
	}
	if version == WorkerNotificationFormatVersion {
		return workerNotificationSnapshotMagic
	}
	if version == WorkerCheckRetentionFormatVersion {
		return workerCheckRetentionSnapshotMagic
	}
	if version == WorkerSchedulingFormatVersion {
		return workerSchedulingSnapshotMagic
	}
	if version == WorkerSLOFormatVersion {
		return workerSLOSnapshotMagic
	}
	if version == WorkerFinalizationFormatVersion {
		return workerFinalizationSnapshotMagic
	}
	if version == WorkerLifecycleFormatVersion {
		return workerLifecycleSnapshotMagic
	}
	if version == WorkerResultFormatVersion {
		return workerResultSnapshotMagic
	}
	if version == WorkerOfferFormatVersion {
		return workerOfferSnapshotMagic
	}
	if version == WorkerExecutionFormatVersion {
		return workerExecutionSnapshotMagic
	}
	if version == WorkerSessionFormatVersion {
		return workerSessionSnapshotMagic
	}
	if version == CatalogJobTypeFormatVersion {
		return catalogJobTypeSnapshotMagic
	}
	if version == WorkerPolicyFormatVersion {
		return workerPolicySnapshotMagic
	}
	if version == JobTypeOperationFormatVersion {
		return jobTypeOperationSnapshotMagic
	}
	if version == JobTypeFormatVersion {
		return jobTypeSnapshotMagic
	}
	return ""
}
func externalSnapshotVersion(prefix []byte) (int, int) {
	if bytes.HasPrefix(prefix, []byte(deliveryWorkerSnapshotMagic)) {
		return DeliveryWorkerFormatVersion, len(deliveryWorkerSnapshotMagic)
	}
	if bytes.HasPrefix(prefix, []byte(notificationWorkerSnapshotMagic)) {
		return NotificationWorkerFormatVersion, len(notificationWorkerSnapshotMagic)
	}
	if bytes.HasPrefix(prefix, []byte(workerQueueSnapshotMagic)) {
		return WorkerQueueFormatVersion, len(workerQueueSnapshotMagic)
	}
	if bytes.HasPrefix(prefix, []byte(agentObservationWorkerSnapshotMagic)) {
		return AgentObservationWorkerFormatVersion, len(agentObservationWorkerSnapshotMagic)
	}
	if bytes.HasPrefix(prefix, []byte(recoveryNotificationWorkerSnapshotMagic)) {
		return RecoveryNotificationWorkerFormatVersion, len(recoveryNotificationWorkerSnapshotMagic)
	}
	if bytes.HasPrefix(prefix, []byte(agentWorkerSnapshotMagic)) {
		return AgentWorkerFormatVersion, len(agentWorkerSnapshotMagic)
	}
	if bytes.HasPrefix(prefix, []byte(workerScheduleSnapshotMagic)) {
		return WorkerScheduleFormatVersion, len(workerScheduleSnapshotMagic)
	}
	if bytes.HasPrefix(prefix, []byte(workerExpirySnapshotMagic)) {
		return WorkerExpiryFormatVersion, len(workerExpirySnapshotMagic)
	}
	if bytes.HasPrefix(prefix, []byte(workerNotificationSnapshotMagic)) {
		return WorkerNotificationFormatVersion, len(workerNotificationSnapshotMagic)
	}
	if bytes.HasPrefix(prefix, []byte(workerCheckRetentionSnapshotMagic)) {
		return WorkerCheckRetentionFormatVersion, len(workerCheckRetentionSnapshotMagic)
	}
	if bytes.HasPrefix(prefix, []byte(workerSchedulingSnapshotMagic)) {
		return WorkerSchedulingFormatVersion, len(workerSchedulingSnapshotMagic)
	}
	if bytes.HasPrefix(prefix, []byte(workerSLOSnapshotMagic)) {
		return WorkerSLOFormatVersion, len(workerSLOSnapshotMagic)
	}
	if bytes.HasPrefix(prefix, []byte(workerFinalizationSnapshotMagic)) {
		return WorkerFinalizationFormatVersion, len(workerFinalizationSnapshotMagic)
	}
	if bytes.HasPrefix(prefix, []byte(workerLifecycleSnapshotMagic)) {
		return WorkerLifecycleFormatVersion, len(workerLifecycleSnapshotMagic)
	}
	if bytes.HasPrefix(prefix, []byte(workerResultSnapshotMagic)) {
		return WorkerResultFormatVersion, len(workerResultSnapshotMagic)
	}
	if bytes.HasPrefix(prefix, []byte(workerOfferSnapshotMagic)) {
		return WorkerOfferFormatVersion, len(workerOfferSnapshotMagic)
	}
	if bytes.HasPrefix(prefix, []byte(workerExecutionSnapshotMagic)) {
		return WorkerExecutionFormatVersion, len(workerExecutionSnapshotMagic)
	}
	if bytes.HasPrefix(prefix, []byte(workerSessionSnapshotMagic)) {
		return WorkerSessionFormatVersion, len(workerSessionSnapshotMagic)
	}
	if bytes.HasPrefix(prefix, []byte(catalogJobTypeSnapshotMagic)) {
		return CatalogJobTypeFormatVersion, len(catalogJobTypeSnapshotMagic)
	}
	if bytes.HasPrefix(prefix, []byte(workerPolicySnapshotMagic)) {
		return WorkerPolicyFormatVersion, len(workerPolicySnapshotMagic)
	}
	if bytes.HasPrefix(prefix, []byte(jobTypeOperationSnapshotMagic)) {
		return JobTypeOperationFormatVersion, len(jobTypeOperationSnapshotMagic)
	}
	if bytes.HasPrefix(prefix, []byte(jobTypeSnapshotMagic)) {
		return JobTypeFormatVersion, len(jobTypeSnapshotMagic)
	}
	return 0, 0
}

func cloneImageExtensions(i image) imageExtensions {
	extensions := imageExtensions{WorkerChecks: i.WorkerChecks.clone(), WorkerExecutions: i.WorkerExecutions.clone(), WorkerSessions: i.WorkerSessions.clone()}
	if i.WorkerSLO != nil {
		copy := *i.WorkerSLO
		extensions.WorkerSLO = &copy
	}
	if i.WorkerPolicy != nil {
		copy := i.WorkerPolicy.Clone()
		extensions.WorkerPolicy = &copy
	}
	if i.JobTypes == nil {
		return extensions
	}
	out := &jobTypeImage{Records: make(map[string]JobTypeState, len(i.JobTypes.Records)), EncodedBytes: i.JobTypes.EncodedBytes}
	for id, state := range i.JobTypes.Records {
		out.Records[id] = state.Clone()
	}
	extensions.JobTypes = out
	return extensions
}

func jobTypeStateCost(s JobTypeState) (int64, error) {
	// Count the exact canonical state encoding without materializing its whole
	// version inventory. Scratch is bounded by one encrypted descriptor. Each
	// quota entry includes its escaped outer key, colon and comma framing.
	key, _ := json.Marshal(s.Current.Record.Key.ID)
	digest, _ := json.Marshal(s.CommandDigest)
	used := int64(len(key) + 2 + len(`{"current":`) + len(`,"versions":{`) + len(`},"command_digest":`) + len(digest) + 1)
	charge := func(value JobTypeVersion) error {
		raw, err := json.Marshal(value)
		if err != nil {
			return ErrJobTypeInvalid
		}
		used += int64(len(raw))
		if used > MaxJobTypeStateBytes {
			return ErrJobTypeQuota
		}
		return nil
	}
	if err := charge(s.Current); err != nil {
		return 0, err
	}
	first := true
	for version, value := range s.Versions {
		encoded, _ := json.Marshal(version)
		used += int64(len(encoded) + 1)
		if !first {
			used++
		}
		first = false
		if err := charge(value); err != nil {
			return 0, err
		}
	}
	return used, nil
}

func sameJobTypeSelectors(a, b JobTypeVersion) bool {
	return a.Version == b.Version && a.Category == b.Category && a.Handler == b.Handler && a.ProtocolVersion == b.ProtocolVersion && a.SchemaProfile == b.SchemaProfile
}

func validateImageExtensions(i image) error {
	if i.Version == AgentActionFormatVersion || i.Version == RecoveryNotificationFormatVersion || i.Version == AgentObservationFormatVersion || i.Version == NotificationFormatVersion || i.Version == DeliveryFormatVersion {
		if i.imageExtensions != (imageExtensions{}) {
			return ErrJobTypeInvalid
		}
		for _, m := range i.Monitors {
			if policyMinimumFormat(m.Policy) != FormatVersion {
				return ErrWorkerExecutionInvalid
			}
		}
		for _, r := range i.Catalog {
			if catalogRecordMinimumFormat(r) != FormatVersion {
				return ErrJobTypeInvalid
			}
		}
	}
	if err := validateWorkerExpiryImage(i); err != nil {
		return err
	}
	if err := validateWorkerChecks(i); err != nil {
		return err
	}
	if err := validateWorkerSLO(i); err != nil {
		return err
	}
	if err := validateWorkerExecutionImage(i); err != nil {
		return err
	}
	if err := validateWorkerSessionImage(i); err != nil {
		return err
	}
	if err := validateWorkerPolicyImage(i); err != nil {
		return err
	}
	for _, r := range i.OperationReservations {
		if r.CommandKind == "job_type" && (i.Version < JobTypeOperationFormatVersion || !externalStorageFormat(i.Version)) {
			return ErrJobTypeInvalid
		}
	}
	if i.JobTypes == nil {
		return nil
	}
	if !externalStorageFormat(i.Version) || i.JobTypes.Records == nil || len(i.JobTypes.Records) > MaxJobTypes {
		return ErrJobTypeInvalid
	}
	var used int64
	for id, state := range i.JobTypes.Records {
		current := state.Current
		if id != current.Record.Key.ID || current.validate() != nil || current.Record.CommittedIndex == 0 || current.Record.CommittedIndex > i.Index || !bootstrapHash(state.CommandDigest) || len(state.Versions) == 0 || len(state.Versions) > MaxJobTypeVersions {
			return ErrJobTypeInvalid
		}
		original, ok := state.Versions[current.Version]
		if !ok || !sameJobTypeSelectors(current, original) || current.Record.UID != original.Record.UID || !current.Record.CreatedAt.Equal(original.Record.CreatedAt) || current.Record.Generation < original.Record.Generation {
			return ErrJobTypeInvalid
		}
		for version, value := range state.Versions {
			if version != value.Version || value.validate() != nil || value.Record.Removed || value.Record.Key != current.Record.Key || value.Category != current.Category || value.Record.CommittedIndex == 0 || value.Record.CommittedIndex > current.Record.CommittedIndex || value.Record.UpdatedAt.After(current.Record.UpdatedAt) {
				return ErrJobTypeInvalid
			}
		}
		cost, err := jobTypeStateCost(state)
		if err != nil {
			return err
		}
		if cost > MaxJobTypeStateBytes || cost > MaxJobTypeTotalBytes-used {
			return ErrJobTypeQuota
		}
		used += cost
	}
	if used != i.JobTypes.EncodedBytes {
		return ErrJobTypeInvalid
	}
	return nil
}

func jobTypeCommandDigest(c JobTypeCommand, at time.Time) string {
	data, _ := json.Marshal(struct {
		Command jobTypeCommandDigestV1 `json:"command"`
		At      time.Time              `json:"at"`
	}{jobTypeCommandV1(c), at.UTC()})
	h := sha256.Sum256(append([]byte("cpra-job-type-command-v1\x00"), data...))
	return hex.EncodeToString(h[:])
}

func (f *machine) applyCommandExtensions(command Command, index uint64, version int) (Result, bool) {
	if command.Kind == "worker_schedule" {
		return f.applyWorkerSchedule(*command.WorkerSchedule, command.At), true
	}
	if command.Kind == "worker_expire" {
		return f.applyWorkerExpiry(*command.WorkerExpire, command.At), true
	}
	if command.Kind == "worker_check_allocate" {
		return f.applyWorkerCheckAllocation(*command.WorkerCheckAllocate, command.At, index), true
	}
	if command.Kind == "worker_check_retire" {
		return f.applyWorkerCheckRetirement(*command.WorkerCheckRetire, command.At), true
	}
	if command.Kind == "worker_requeue" {
		return f.applyWorkerRequeue(*command.WorkerRequeue, command.At), true
	}
	if command.Kind == "worker_observation" {
		return f.applyWorkerObservation(*command.WorkerObservation, command.At, index), true
	}
	if command.Kind == "worker_finalize" {
		return f.applyWorkerFinalization(*command.WorkerFinalize, command.At, index), true
	}
	if command.Kind == "worker_evidence" {
		return f.applyWorkerEvidence(*command.WorkerEvidence, command.At, index), true
	}
	if command.Kind == "worker_heartbeat" {
		return f.applyWorkerHeartbeat(*command.WorkerHeartbeat, command.At), true
	}
	if command.Kind == "worker_result" {
		return f.applyWorkerResult(*command.WorkerResult, command.At, index, version), true
	}
	if command.Kind == "worker_start" {
		return f.applyWorkerStart(*command.WorkerStart, command.At, version), true
	}
	if command.Kind == "worker_execution" {
		return f.applyWorkerExecution(*command.WorkerExecution, command.At, index), true
	}
	if command.Kind == "worker_poll" {
		return f.applyWorkerPoll(*command.WorkerSession, command.At), true
	}
	if command.Kind == "worker_policy" {
		return f.applyWorkerPolicy(*command.WorkerPolicy), true
	}
	if command.Kind == "job_type_reserve" {
		return f.applyJobTypeOperationAllocation(*command.JobTypeAllocation, command.At), true
	}
	if command.Kind != "job_type" {
		return Result{}, false
	}
	c := *command.JobType
	if err := f.checkOperatorAuthority(c.Authority, command.At); err != nil {
		return Result{Err: err}, true
	}
	if c.OperationID != "" {
		return f.applyJobTypeOperation(c, command.At, index), true
	}
	return f.applyJobType(c, command.At, index, jobTypeCommandDigest(c, command.At)), true
}

func (f *machine) applyJobType(c JobTypeCommand, at time.Time, index uint64, digest string) Result {
	var old JobTypeState
	var exists bool
	var total int64
	if f.image.JobTypes != nil {
		old, exists = f.image.JobTypes.Records[c.Value.Record.Key.ID]
		total = f.image.JobTypes.EncodedBytes
	}
	if exists && old.CommandDigest == digest {
		copy := old.Clone()
		return Result{Allowed: true, resultExtensions: resultExtensions{JobType: &copy}}
	}
	if exists && (at.Before(old.Current.Record.UpdatedAt) || c.Value.Record.UpdatedAt.Before(old.Current.Record.UpdatedAt)) {
		return Result{Err: ErrJobTypeConflict}
	}
	value := c.Value.Clone()
	value.Record.CommittedIndex = index
	retained := false
	switch c.Action {
	case "create":
		if exists && (!old.Current.Record.Removed || old.Current.Record.UID == value.Record.UID || old.Current.Record.Revision == value.Record.Revision) {
			return Result{Err: ErrJobTypeConflict}
		}
		if !exists && f.image.JobTypes != nil && len(f.image.JobTypes.Records) >= MaxJobTypes {
			return Result{Err: ErrJobTypeQuota}
		}
		for _, retained := range old.Versions {
			if retained.Record.UID == value.Record.UID {
				return Result{Err: ErrJobTypeConflict}
			}
		}
	case "replace", "delete":
		if !exists || old.Current.Record.Removed || old.Current.Record.UID != c.ExpectedUID || old.Current.Record.Revision != c.ExpectedRevision || !old.Current.Record.CreatedAt.Equal(value.Record.CreatedAt) || old.Current.Record.Generation >= math.MaxInt64 || value.Record.Generation != old.Current.Record.Generation+1 {
			return Result{Err: ErrJobTypeConflict}
		}
	}
	if exists && value.Category != old.Current.Category {
		return Result{Err: ErrJobTypeVersionConflict}
	}
	for _, retained := range old.Versions {
		if retained.Record.Revision == value.Record.Revision {
			return Result{Err: ErrJobTypeConflict}
		}
	}
	if c.Action == "delete" {
		if f.jobTypeExecutionReferenced(value.Record.Key.ID, value.Record.UID) || f.jobTypeWorkerReferenced(value.Record.Key.ID, value.Record.UID) || f.jobTypeCatalogReferenced(value.Record.Key.ID, value.Record.UID) {
			return Result{Err: ErrCatalogReferenced}
		}
		if !sameJobTypeSelectors(value, old.Current) {
			return Result{Err: ErrJobTypeVersionConflict}
		}
	} else if original, ok := old.Versions[value.Version]; ok {
		if c.Action != "replace" || old.Current.Version != value.Version || c.RetainedVersionRevision != original.Record.Revision || !sameJobTypeSelectors(value, original) {
			return Result{Err: ErrJobTypeVersionConflict}
		}
		retained = true
	} else if c.RetainedVersionRevision != "" {
		return Result{Err: ErrJobTypeVersionConflict}
	}
	if c.Action != "delete" && !retained && len(old.Versions) >= MaxJobTypeVersions {
		return Result{Err: ErrJobTypeQuota}
	}
	// Records are immutable after admission. Clone only the map for this update;
	// readers/snapshots receive their own ciphertext slices.
	next := JobTypeState{Current: value, Versions: make(map[string]JobTypeVersion, len(old.Versions)+1), CommandDigest: digest}
	for version, v := range old.Versions {
		next.Versions[version] = v
	}
	if c.Action != "delete" && !retained {
		next.Versions[value.Version] = value
	}
	var previousCost int64
	if exists {
		previousCost, _ = jobTypeStateCost(old)
	}
	cost, err := jobTypeStateCost(next)
	if err != nil {
		return Result{Err: err}
	}
	if cost > MaxJobTypeStateBytes || cost > MaxJobTypeTotalBytes-(total-previousCost) {
		return Result{Err: ErrJobTypeQuota}
	}
	if f.image.JobTypes == nil {
		f.image.JobTypes = &jobTypeImage{Records: make(map[string]JobTypeState)}
	}
	f.image.JobTypes.Records[value.Record.Key.ID] = next
	f.image.JobTypes.EncodedBytes = total - previousCost + cost
	version := JobTypeFormatVersion
	if c.OperationID != "" {
		version = JobTypeOperationFormatVersion
	}
	f.image.Version = max(f.image.Version, version)
	copy := next.Clone()
	return Result{Allowed: true, resultExtensions: resultExtensions{JobType: &copy}}
}

// CommitJobType returns the detached state from this exact committed command.
// An uncertain submission must be reconciled by identity, not re-encryption.
func (s *Store) CommitJobType(ctx context.Context, c JobTypeCommand, at time.Time) (JobTypeState, error) {
	if c.OperationID != "" {
		return JobTypeState{}, ErrOperationReservation
	}
	results, err := s.Submit(ctx, []Command{{commandExtensions: commandExtensions{JobType: &c}, Kind: "job_type", At: at}})
	if err != nil {
		return JobTypeState{}, err
	}
	if len(results) != 1 {
		return JobTypeState{}, errors.Join(ErrCommitUnconfirmed, ErrJobTypeUnavailable)
	}
	if results[0].Err != nil {
		return JobTypeState{}, results[0].Err
	}
	if !results[0].Allowed || results[0].JobType == nil {
		return JobTypeState{}, errors.Join(ErrCommitUnconfirmed, ErrJobTypeUnavailable)
	}
	return *results[0].JobType, nil
}

// JobType returns current metadata, tombstones and immutable versions for
// preparation/startup verification. Authentication belongs to the caller.
func (s *Store) JobType(ctx context.Context, id string) (JobTypeState, bool, error) {
	if !catalogIdentifier(id, 256) {
		return JobTypeState{}, false, ErrJobTypeInvalid
	}
	unlock, err := s.lockCatalogReadState(ctx, false)
	if err != nil {
		return JobTypeState{}, false, err
	}
	defer unlock()
	if s.fsm.image.JobTypes == nil {
		return JobTypeState{}, false, nil
	}
	state, ok := s.fsm.image.JobTypes.Records[id]
	if !ok {
		return JobTypeState{}, false, nil
	}
	copy := state.Clone()
	if err := ctx.Err(); err != nil {
		return JobTypeState{}, false, err
	}
	return copy, true, nil
}

// JobTypeVersion reads one retained immutable encrypted version, even when its
// current descriptor is deleted. It confers no permission to execute it.
func (s *Store) JobTypeVersion(ctx context.Context, id, version string) (JobTypeVersion, bool, error) {
	if !catalogIdentifier(id, 256) || !jobTypeSelector(version) {
		return JobTypeVersion{}, false, ErrJobTypeInvalid
	}
	unlock, err := s.lockCatalogReadState(ctx, false)
	if err != nil {
		return JobTypeVersion{}, false, err
	}
	defer unlock()
	if s.fsm.image.JobTypes == nil {
		return JobTypeVersion{}, false, nil
	}
	v, ok := s.fsm.image.JobTypes.Records[id].Versions[version]
	if !ok {
		return JobTypeVersion{}, false, nil
	}
	copy := v.Clone()
	if err := ctx.Err(); err != nil {
		return JobTypeVersion{}, false, err
	}
	return copy, true, nil
}

// JobTypes pages active current descriptors in ID order, bounded to 100 rows
// and 4 MiB canonical encoded bytes. It does not copy old version inventories.
func (s *Store) JobTypes(ctx context.Context, after string, limit int) ([]JobTypeVersion, string, error) {
	if limit < 1 || limit > 100 || after != "" && !catalogIdentifier(after, 256) {
		return nil, "", ErrJobTypeInvalid
	}
	unlock, err := s.lockCatalogReadState(ctx, false)
	if err != nil {
		return nil, "", err
	}
	defer unlock()
	if s.fsm.image.JobTypes == nil {
		return nil, "", nil
	}
	ids := make([]string, 0, len(s.fsm.image.JobTypes.Records))
	for id, state := range s.fsm.image.JobTypes.Records {
		if id > after && !state.Current.Record.Removed {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	items := make([]JobTypeVersion, 0, min(limit, len(ids)))
	used := 0
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		value := s.fsm.image.JobTypes.Records[id].Current
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, "", ErrJobTypeUnavailable
		}
		if len(items) == limit || len(encoded)+4 > MaxJobTypePageBytes-used {
			if len(items) == 0 {
				return nil, "", ErrJobTypeQuota
			}
			return items, items[len(items)-1].Record.Key.ID, nil
		}
		used += len(encoded) + 4
		items = append(items, value.Clone())
	}
	return items, "", ctx.Err()
}

// JobTypeIDs includes tombstones so startup can authenticate every retained
// version. It copies at most MaxJobTypes identifiers and no ciphertext.
func (s *Store) JobTypeIDs(ctx context.Context) ([]string, error) {
	unlock, err := s.lockCatalogReadState(ctx, false)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if s.fsm.image.JobTypes == nil {
		return nil, nil
	}
	ids := make([]string, 0, len(s.fsm.image.JobTypes.Records))
	for id := range s.fsm.image.JobTypes.Records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, ctx.Err()
}
