// Package persistence implements Raft storage and deterministic state transitions.
// Runtime jobs, provider configuration, clients and ECS entity IDs never enter it.
package persistence

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/ziad-hsn/cpra/internal/slo"
	"maps"
	"time"
)

const FormatVersion = 1

// LatestFormatVersion is the highest application storage format this binary can
// read/write. A store remains at its existing version until a newer feature is
// committed. Release metadata records this compatibility boundary, not the
// legacy incident-only FormatVersion.

type ActionState string

const (
	Queued    ActionState = "queued"
	Started   ActionState = "started"
	Succeeded ActionState = "succeeded"
	Failed    ActionState = "failed"
	Unknown   ActionState = "unknown"
	Cancelled ActionState = "cancelled"
)

// Policy is deliberately an allowlist of non-secret decision inputs.
type Policy struct {
	policyExtensions
	Interval       time.Duration  `json:"interval"`
	Unhealthy      int            `json:"unhealthy"`
	Healthy        int            `json:"healthy"`
	Intervention   bool           `json:"intervention"`
	Enabled        bool           `json:"enabled"`
	Cooldown       time.Duration  `json:"cooldown"`
	RecoveryBypass bool           `json:"recovery_bypass"`
	Endpoints      map[string]int `json:"endpoints,omitempty"`
	// Zero retains the legacy one-attempt policy; cooldown applies only to a
	// subsequent attempt after a confirmed recovery failure.
	RecoveryCooldown    time.Duration       `json:"recovery_cooldown,omitempty"`
	RecoveryMaxAttempts int                 `json:"recovery_max_attempts,omitempty"`
	Maintenance         []MaintenanceWindow `json:"maintenance,omitempty"`
}

type Action struct {
	ConflictingEvidence *LateActionEvidence `json:"conflicting_evidence,omitempty"`
	OperationID         string              `json:"operation_id,omitempty"`
	Manual              bool                `json:"manual,omitempty"`
	Review              *ActionReview       `json:"review,omitempty"`
	ExecutorKind        string              `json:"executor_kind,omitempty"`
	ExecutorSession     string              `json:"executor_session,omitempty"`
	ExecutorFinishedAt  time.Time           `json:"executor_finished_at,omitempty"`
	IncidentID          string              `json:"incident_id,omitempty"`
	LateEvidence        *LateActionEvidence `json:"late_evidence,omitempty"`
	CatalogUID          string              `json:"catalog_uid,omitempty"`
	ID                  string              `json:"id"`
	Revision            string              `json:"revision"`
	Kind                string              `json:"kind"`
	Color               string              `json:"color,omitempty"`
	Endpoint            int                 `json:"endpoint"`
	Attempt             int                 `json:"attempt"`
	State               ActionState         `json:"state"`
	NotBefore           time.Time           `json:"not_before"`
	StartedAt           time.Time           `json:"started_at,omitempty"`
	FinishedAt          time.Time           `json:"finished_at,omitempty"`
	Outcome             string              `json:"outcome,omitempty"`
}

type Monitor struct {
	ownerObserved           ownerObservation     // Process-local acknowledgement; never serialized.
	DependencyRevision      string               `json:"dependency_revision,omitempty"`
	ManualRequests          []time.Time          `json:"manual_requests,omitempty"`
	unknownActions          int                  // Rebuilt after restoration; compact observation cache, never authoritative.
	CatalogRevision         string               `json:"catalog_revision,omitempty"`
	ControlRevision         string               `json:"control_revision,omitempty"`
	SnoozedUntil            time.Time            `json:"snoozed_until,omitempty"`
	SnoozedBy               string               `json:"snoozed_by,omitempty"`
	SnoozeReason            string               `json:"snooze_reason,omitempty"`
	IncidentID              string               `json:"incident_id,omitempty"`
	IncidentRevision        string               `json:"incident_revision,omitempty"`
	IncidentSequence        uint64               `json:"incident_sequence,omitempty"`
	IncidentOpenedAt        time.Time            `json:"incident_opened_at,omitempty"`
	IncidentClosedAt        time.Time            `json:"incident_closed_at,omitempty"`
	AcknowledgedBy          string               `json:"acknowledged_by,omitempty"`
	AcknowledgedAt          time.Time            `json:"acknowledged_at,omitempty"`
	AcknowledgedNote        string               `json:"acknowledged_note,omitempty"`
	Dismissed               bool                 `json:"dismissed,omitempty"`
	DismissedBy             string               `json:"dismissed_by,omitempty"`
	DismissedAt             time.Time            `json:"dismissed_at,omitempty"`
	DismissedReason         string               `json:"dismissed_reason,omitempty"`
	CatalogUID              string               `json:"catalog_uid,omitempty"`
	Removed                 bool                 `json:"removed"`
	TotalChecks             uint64               `json:"total_checks"`
	SuccessfulChecks        uint64               `json:"successful_checks"`
	ID                      string               `json:"monitor_id"`
	Revision                string               `json:"revision"`
	Name                    string               `json:"name"`
	Policy                  Policy               `json:"policy"`
	Sequence                uint64               `json:"sequence"`
	Generation              uint64               `json:"generation"`
	Incident                bool                 `json:"incident"`
	Recovering              bool                 `json:"recovering"`
	InterventionAttempted   bool                 `json:"intervention_attempted"`
	InterventionAttempts    int                  `json:"intervention_attempts,omitempty"`
	LastInterventionFailure time.Time            `json:"last_intervention_failure,omitempty"`
	InterventionFailures    int                  `json:"intervention_failures"`
	ConsecutiveFailures     int                  `json:"consecutive_failures"`
	PulseFailures           int                  `json:"pulse_failures"`
	RecoveryStreak          int                  `json:"recovery_streak"`
	VerifyRemaining         int                  `json:"verify_remaining"`
	VerificationAfter       uint64               `json:"verification_after"`
	LastCheck               time.Time            `json:"last_check"`
	LastSuccess             time.Time            `json:"last_success"`
	NextCheck               time.Time            `json:"next_check"`
	LastLatency             time.Duration        `json:"last_latency"`
	LatencyAvailable        bool                 `json:"latency_available"`
	Warning                 bool                 `json:"warning"`
	LastOutcome             string               `json:"last_outcome,omitempty"`
	Actions                 map[string]Action    `json:"actions,omitempty"`
	Cooldowns               map[string]time.Time `json:"cooldowns,omitempty"`
}

func (m Monitor) Clone() Monitor {
	m.ManualRequests = append([]time.Time(nil), m.ManualRequests...)
	m.Policy = m.Policy.Clone()
	m.Actions = maps.Clone(m.Actions)
	for id, action := range m.Actions {
		m.Actions[id] = action.Clone()
	}
	m.Cooldowns = maps.Clone(m.Cooldowns)
	return m
}

// Command includes observation time and identity at submission, never on replay.
type Command struct {
	commandExtensions
	CollectionExecute    *CollectionExecuteCommand `json:"collection_execute,omitempty"`
	Collection           *CollectionCommand        `json:"collection,omitempty"`
	OperationAllocation  *OperationAllocation      `json:"operation_allocation,omitempty"`
	Authentication       *AuthenticationCommand    `json:"authentication,omitempty"`
	Restore              *RestoreCommand           `json:"restore,omitempty"`
	ManualRecovery       *ManualRecoveryCommand    `json:"manual_recovery,omitempty"`
	ActionReview         *ActionReviewCommand      `json:"action_review,omitempty"`
	ExecutorSession      string                    `json:"executor_session,omitempty"`
	Control              *ControlCommand           `json:"control,omitempty"`
	CheckControlRevision string                    `json:"check_control_revision,omitempty"`
	Bootstrap            *BootstrapCommand         `json:"bootstrap,omitempty"`
	Guard                *CatalogGuard             `json:"guard,omitempty"`
	Operation            *OperationUpdate          `json:"operation,omitempty"`
	Catalog              *CatalogMutation          `json:"catalog,omitempty"`
	SLO                  *slo.State                `json:"slo,omitempty"`
	Kind                 string                    `json:"kind"`
	MonitorID            string                    `json:"monitor_id,omitempty"`
	Revision             string                    `json:"revision,omitempty"`
	At                   time.Time                 `json:"at"`
	Config               *Monitor                  `json:"config,omitempty"`
	Generation           uint64                    `json:"generation,omitempty"`
	ActionID             string                    `json:"action_id,omitempty"`
	Outcome              string                    `json:"outcome,omitempty"`
	Retryable            bool                      `json:"retryable,omitempty"`
	Ambiguous            bool                      `json:"ambiguous,omitempty"`
	Maintenance          bool                      `json:"maintenance,omitempty"`
	Warning              bool                      `json:"warning,omitempty"`
	Scheduled            time.Time                 `json:"scheduled,omitempty"`
	ExecutionStart       time.Time                 `json:"execution_start,omitempty"`
	ExecutionEnd         time.Time                 `json:"execution_end,omitempty"`
	Missed               uint64                    `json:"missed,omitempty"`
	Driver               string                    `json:"driver,omitempty"`
}

type Event struct {
	CollectionExecutionHistory *CollectionExecutionHistory  `json:"collection_execution_history,omitempty"`
	CollectionExecution        *CollectionExecutionSummary  `json:"collection_execution,omitempty"`
	CollectionValidation       *CollectionValidationHistory `json:"collection_validation,omitempty"`
	Collection                 *CollectionReceipt           `json:"collection,omitempty"`
	EvidenceRefs               []string                     `json:"evidence_refs,omitempty"`
	IncidentID                 string                       `json:"incident_id,omitempty"`
	Actor                      string                       `json:"actor,omitempty"`
	Reason                     string                       `json:"reason,omitempty"`
	Note                       string                       `json:"note,omitempty"`
	ControlRevision            string                       `json:"control_revision,omitempty"`
	CatalogUID                 string                       `json:"catalog_uid,omitempty"`
	Operation                  *OperationReceipt            `json:"operation,omitempty"`
	ID                         string                       `json:"id"`
	MonitorID                  string                       `json:"monitor_id"`
	Revision                   string                       `json:"revision"`
	At                         time.Time                    `json:"at"`
	Type                       string                       `json:"type"`
	ActionID                   string                       `json:"action_id,omitempty"`
	Kind                       string                       `json:"kind,omitempty"`
	Color                      string                       `json:"color,omitempty"`
	Endpoint                   int                          `json:"endpoint,omitempty"`
	Outcome                    string                       `json:"outcome,omitempty"`
}

type Result struct {
	resultExtensions
	// CatalogMutationSequence is an atomic own-mutation token in format 4.
	// Zero means no such token; legacy CommittedIndex is not a substitute.
	CatalogMutationSequence uint64
	CollectionID            string
	CollectionEpoch         string
	Collection              *CollectionState
	Reservation             *OperationReservation
	Authentication          *AuthenticationState
	Operation               *OperationReceipt
	Catalog                 *CatalogRecord
	Monitor                 *Monitor
	Events                  []Event
	Allowed                 bool
	Err                     error
}

func identity(parts string) string {
	h := sha256.Sum256([]byte(parts))
	return hex.EncodeToString(h[:])
}

func validateCommand(c Command) error {
	if c.At.IsZero() {
		return fmt.Errorf("command observation time is required")
	}
	if handled, err := validateCommandExtensions(c); handled || err != nil {
		return err
	}
	if c.Kind == "collection_execute" {
		extra := c
		extra.Kind, extra.At, extra.CollectionExecute = "", time.Time{}, nil
		if extra != (Command{}) || c.CollectionExecute == nil {
			return ErrCollectionInvalid
		}
		return c.CollectionExecute.validate(c.At)
	}
	if c.CollectionExecute != nil {
		return ErrCollectionInvalid
	}
	if c.Kind == "collection" {
		extra := c
		extra.Kind, extra.At, extra.Collection = "", time.Time{}, nil
		if extra != (Command{}) || c.Collection == nil {
			return ErrCollectionInvalid
		}
		return c.Collection.validate(c.At)
	}
	if c.Collection != nil {
		return ErrCollectionInvalid
	}
	if c.Kind == "operation_reserve" || c.Kind == "operation_expire" {
		extra := c
		extra.Kind, extra.At, extra.OperationAllocation = "", time.Time{}, nil
		if extra != (Command{}) {
			return ErrOperationReservation
		}
		if c.Kind == "operation_expire" {
			if c.OperationAllocation != nil {
				return ErrOperationReservation
			}
			return nil
		}
		if c.OperationAllocation == nil || !c.At.Equal(c.OperationAllocation.Reservation.At) {
			return ErrOperationReservation
		}
		return validateOperationAllocationCommand(*c.OperationAllocation)
	}
	if c.OperationAllocation != nil {
		return ErrOperationReservation
	}
	if c.Kind == "authentication" || c.Kind == "restore_reset" {
		extra := c
		extra.Kind, extra.At = "", time.Time{}
		if c.Kind == "authentication" {
			if c.Authentication == nil || !c.At.Equal(c.Authentication.At) || c.Authentication.validate() != nil {
				return ErrAuthenticationInvalid
			}
			extra.Authentication = nil
		} else {
			if c.Restore == nil || !c.At.Equal(c.Restore.Marker.At) || c.Restore.validate() != nil {
				return ErrRestoreInvalid
			}
			extra.Restore = nil
		}
		if extra != (Command{}) {
			return ErrAuthenticationInvalid
		}
		return nil
	}
	if c.Authentication != nil || c.Restore != nil {
		return ErrAuthenticationInvalid
	}
	if c.Kind == "manual_recovery" || c.Kind == "action_review" {
		return validateRecoveryReviewCommand(c)
	}
	if c.ManualRecovery != nil || c.ActionReview != nil {
		return ErrControlInvalid
	}
	if c.Kind == "local_session" || c.Kind == "executor_finished" {
		if !catalogIdentifier(c.ExecutorSession, 256) {
			return ErrExecutorUnfenced
		}
		extra := c
		extra.Kind, extra.At, extra.ExecutorSession = "", time.Time{}, ""
		if c.Kind == "executor_finished" {
			if !catalogIdentifier(c.MonitorID, 256) || !catalogIdentifier(c.ActionID, 256) || !catalogIdentifier(c.Revision, 256) {
				return ErrExecutorUnfenced
			}
			extra.MonitorID, extra.ActionID, extra.Revision = "", "", ""
		}
		if extra != (Command{}) {
			return ErrExecutorUnfenced
		}
		return nil
	}
	if c.ExecutorSession != "" && c.Kind != "start" {
		return ErrExecutorUnfenced
	}
	if c.Kind == "control" {
		if c.Control == nil || !catalogIdentifier(c.MonitorID, 256) {
			return ErrControlInvalid
		}
		extra := c
		extra.Kind, extra.MonitorID, extra.At, extra.Control = "", "", time.Time{}, nil
		if extra != (Command{}) {
			return ErrControlInvalid
		}
		return c.Control.validate(c.At)
	}
	if c.Control != nil || (c.CheckControlRevision != "" && c.Kind != "pulse") {
		return ErrControlInvalid
	}
	if c.Kind == "barrier" {
		extra := c
		extra.Kind, extra.At = "", time.Time{}
		if extra != (Command{}) {
			return errors.New("barrier contains unrelated fields")
		}
		return nil
	}
	if c.Kind == "bootstrap" {
		if c.Bootstrap == nil {
			return ErrBootstrapConflict
		}
		extra := c
		extra.Kind, extra.At, extra.Bootstrap = "", time.Time{}, nil
		if extra != (Command{}) {
			return errors.New("bootstrap command contains unrelated fields")
		}
		return c.Bootstrap.validate()
	}
	if c.Bootstrap != nil {
		return errors.New("unexpected bootstrap data on lifecycle command")
	}
	if c.Kind == "catalog" {
		if c.Catalog == nil {
			return fmt.Errorf("catalog mutation is required")
		}
		// Catalog commands carry provider configuration only inside the encrypted
		// envelope. Reject unrelated lifecycle fields instead of persisting them
		// as ignored plaintext alongside an otherwise valid catalog mutation.
		extra := c
		extra.Kind, extra.At, extra.Catalog = "", time.Time{}, nil
		if extra != (Command{}) {
			return fmt.Errorf("catalog command contains unrelated lifecycle fields")
		}
		return c.Catalog.validate()
	}
	if c.Kind == "operation" {
		if c.Operation == nil {
			return fmt.Errorf("operation progress is required")
		}
		extra := c
		extra.Kind, extra.At, extra.Operation = "", time.Time{}, nil
		if extra != (Command{}) {
			return fmt.Errorf("operation command contains unrelated fields")
		}
		return c.Operation.validate()
	}
	if c.Operation != nil {
		return fmt.Errorf("unexpected operation progress on lifecycle command")
	}
	if c.Catalog != nil {
		return fmt.Errorf("unexpected catalog mutation on lifecycle command")
	}
	if c.Guard != nil {
		if c.Kind != "configure" && c.Kind != "start" && c.Kind != "pulse" && c.Kind != "remove" {
			return fmt.Errorf("unexpected execution guard on lifecycle command")
		}
		if (c.Kind == "remove") != c.Guard.Removed {
			return fmt.Errorf("execution guard has the wrong removal disposition")
		}
		if err := c.Guard.validate(c.MonitorID); err != nil {
			return err
		}
	}
	if c.Kind == "recover" {
		return nil
	}
	if c.Kind == "slo" {
		if c.SLO == nil || c.SLO.ByDriver == nil {
			return fmt.Errorf("missing SLO aggregate")
		}
		return c.SLO.Validate()
	}
	if c.MonitorID == "" || c.Revision == "" {
		return fmt.Errorf("monitor identity and revision are required")
	}
	if len(c.MonitorID) > 256 {
		return fmt.Errorf("monitor identity exceeds 256 bytes")
	}
	switch c.Kind {
	case "configure":
		if c.Config == nil || c.Config.ID != c.MonitorID || c.Config.Revision != c.Revision || c.Config.Policy.Interval <= 0 {
			return fmt.Errorf("invalid durable monitor configuration")
		}
		if err := c.Config.Policy.Validate(); err != nil {
			return err
		}
		if c.Config.CatalogUID != "" && (c.Guard == nil || c.Config.CatalogUID != c.Guard.monitorUID(c.MonitorID)) {
			return fmt.Errorf("durable monitor incarnation requires its matching guard")
		}
	case "remove":
	case "pulse":
		if c.Generation == 0 {
			return fmt.Errorf("pulse generation is required")
		}
	case "start", "result":
		if c.ActionID == "" {
			return fmt.Errorf("action identity is required")
		}
	case "late_result":
		return validateLateResult(c)
	default:
		return fmt.Errorf("unknown durable command %q", c.Kind)
	}
	return nil
}
