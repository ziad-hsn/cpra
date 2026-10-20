// Package persistence implements Raft storage and deterministic state transitions.
// Runtime jobs, plaintext provider configuration, clients and ECS entity IDs never enter it.
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
	AutoIgnore              *AutoIgnorePolicy          `json:"auto_ignore,omitempty"`
	Interval                time.Duration              `json:"interval"`
	Unhealthy               int                        `json:"unhealthy"`
	Healthy                 int                        `json:"healthy"`
	Intervention            bool                       `json:"intervention"`
	Enabled                 bool                       `json:"enabled"`
	Cooldown                time.Duration              `json:"cooldown"`
	RecoveryBypass          bool                       `json:"recovery_bypass"`
	Endpoints               map[string]int             `json:"endpoints,omitempty"`
	NotificationEndpointIDs map[string][]string        `json:"notification_endpoint_ids,omitempty"`
	NotificationTimeouts    map[string][]time.Duration `json:"notification_timeouts,omitempty"`
	NotificationTriggers    map[string]string          `json:"notification_triggers,omitempty"`
	RecoveryDriver          string                     `json:"recovery_driver,omitempty"`
	// Zero retains the legacy one-attempt policy; cooldown applies only to a
	// subsequent attempt after a confirmed recovery failure.
	RecoveryCooldown    time.Duration       `json:"recovery_cooldown,omitempty"`
	RecoveryTimeout     time.Duration       `json:"recovery_timeout,omitempty"`
	RecoveryMaxAttempts int                 `json:"recovery_max_attempts,omitempty"`
	Maintenance         []MaintenanceWindow `json:"maintenance,omitempty"`
	PeriodicMaintenance bool                `json:"periodic_maintenance,omitempty"`
}

type Action struct {
	Retryable                  bool                `json:"retryable,omitempty"`
	ProviderCorrelationVersion int                 `json:"provider_correlation_version,omitempty"`
	Control                    *ControlNotice      `json:"control,omitempty"`
	FileIncarnation            string              `json:"file_incarnation,omitempty"`
	Cause                      FailureDiagnostic   `json:"cause,omitzero"`
	Failure                    FailureDiagnostic   `json:"failure,omitzero"`
	NotificationCorrelation    string              `json:"notification_correlation,omitempty"`
	DeliveryID                 string              `json:"delivery_id,omitempty"`
	NotificationEpisode        string              `json:"notification_episode,omitempty"`
	EndpointID                 string              `json:"endpoint_id,omitempty"`
	QueuedAt                   time.Time           `json:"queued_at,omitzero"`
	Deadline                   time.Time           `json:"deadline,omitzero"`
	AgentConnection            *AgentConnection    `json:"agent_connection,omitempty"`
	RecoveryFailure            *RecoveryFailure    `json:"recovery_failure,omitempty"`
	AgentReceipt               *AgentReceipt       `json:"agent_receipt,omitempty"`
	ConflictingEvidence        *LateActionEvidence `json:"conflicting_evidence,omitempty"`
	OperationID                string              `json:"operation_id,omitempty"`
	Manual                     bool                `json:"manual,omitempty"`
	Review                     *ActionReview       `json:"review,omitempty"`
	ExecutorKind               string              `json:"executor_kind,omitempty"`
	ExecutorSession            string              `json:"executor_session,omitempty"`
	ExecutorFinishedAt         time.Time           `json:"executor_finished_at,omitempty"`
	IncidentID                 string              `json:"incident_id,omitempty"`
	LateEvidence               *LateActionEvidence `json:"late_evidence,omitempty"`
	CatalogUID                 string              `json:"catalog_uid,omitempty"`
	ID                         string              `json:"id"`
	Revision                   string              `json:"revision"`
	Kind                       string              `json:"kind"`
	Color                      string              `json:"color,omitempty"`
	Endpoint                   int                 `json:"endpoint"`
	Attempt                    int                 `json:"attempt"`
	State                      ActionState         `json:"state"`
	NotBefore                  time.Time           `json:"not_before"`
	StartedAt                  time.Time           `json:"started_at,omitempty"`
	FinishedAt                 time.Time           `json:"finished_at,omitempty"`
	Outcome                    string              `json:"outcome,omitempty"`
}

type Monitor struct {
	ActionVersion              int                  `json:"action_version,omitempty"`
	ProviderCorrelationVersion int                  `json:"provider_correlation_version,omitempty"`
	EnabledControl             string               `json:"enabled_control,omitempty"`
	Ignored                    bool                 `json:"ignored,omitempty"`
	IgnoredAt                  time.Time            `json:"ignored_at,omitzero"`
	IgnoredBy                  string               `json:"ignored_by,omitempty"`
	IgnoreReason               string               `json:"ignore_reason,omitempty"`
	IgnoreCause                string               `json:"ignore_cause,omitempty"`
	IgnoreHealthyChecks        int                  `json:"ignore_healthy_checks,omitempty"`
	IgnoreIncidents            []time.Time          `json:"ignore_incidents,omitempty"`
	IgnoreCountedIncident      string               `json:"ignore_counted_incident,omitempty"`
	FileIncarnation            string               `json:"file_incarnation,omitempty"`
	LifecycleVersion           int                  `json:"lifecycle_version,omitempty"`
	NotificationAttempted      string               `json:"notification_attempted,omitempty"`
	RecoveryCheckAfter         time.Time            `json:"recovery_check_after,omitzero"`
	LastFailure                FailureDiagnostic    `json:"last_failure,omitzero"`
	NotificationCorrelation    string               `json:"notification_correlation,omitempty"`
	NotificationVersion        int                  `json:"notification_version,omitempty"`
	NotificationEpisode        string               `json:"notification_episode,omitempty"`
	NotificationMaintenance    bool                 `json:"notification_maintenance,omitempty"`
	ownerObserved              ownerObservation     // Process-local acknowledgement; never serialized.
	DependencyRevision         string               `json:"dependency_revision,omitempty"`
	ManualRequests             []time.Time          `json:"manual_requests,omitempty"`
	heldActions                int                  // Rebuilt observation count; never authoritative.
	unknownActions             int                  // Rebuilt after restoration; compact observation cache, never authoritative.
	CatalogRevision            string               `json:"catalog_revision,omitempty"`
	ControlRevision            string               `json:"control_revision,omitempty"`
	SnoozedUntil               time.Time            `json:"snoozed_until,omitempty"`
	SnoozedBy                  string               `json:"snoozed_by,omitempty"`
	SnoozeReason               string               `json:"snooze_reason,omitempty"`
	IncidentID                 string               `json:"incident_id,omitempty"`
	IncidentRevision           string               `json:"incident_revision,omitempty"`
	IncidentSequence           uint64               `json:"incident_sequence,omitempty"`
	IncidentOpenedAt           time.Time            `json:"incident_opened_at,omitempty"`
	IncidentClosedAt           time.Time            `json:"incident_closed_at,omitempty"`
	AcknowledgedBy             string               `json:"acknowledged_by,omitempty"`
	AcknowledgedAt             time.Time            `json:"acknowledged_at,omitempty"`
	AcknowledgedNote           string               `json:"acknowledged_note,omitempty"`
	Dismissed                  bool                 `json:"dismissed,omitempty"`
	DismissedBy                string               `json:"dismissed_by,omitempty"`
	DismissedAt                time.Time            `json:"dismissed_at,omitempty"`
	DismissedReason            string               `json:"dismissed_reason,omitempty"`
	CatalogUID                 string               `json:"catalog_uid,omitempty"`
	Removed                    bool                 `json:"removed"`
	TotalChecks                uint64               `json:"total_checks"`
	SuccessfulChecks           uint64               `json:"successful_checks"`
	ID                         string               `json:"monitor_id"`
	Revision                   string               `json:"revision"`
	Name                       string               `json:"name"`
	Policy                     Policy               `json:"policy"`
	Sequence                   uint64               `json:"sequence"`
	Generation                 uint64               `json:"generation"`
	Incident                   bool                 `json:"incident"`
	Recovering                 bool                 `json:"recovering"`
	InterventionAttempted      bool                 `json:"intervention_attempted"`
	InterventionAttempts       int                  `json:"intervention_attempts,omitempty"`
	LastInterventionFailure    time.Time            `json:"last_intervention_failure,omitempty"`
	InterventionFailures       int                  `json:"intervention_failures"`
	ConsecutiveFailures        int                  `json:"consecutive_failures"`
	PulseFailures              int                  `json:"pulse_failures"`
	RecoveryStreak             int                  `json:"recovery_streak"`
	VerifyRemaining            int                  `json:"verify_remaining"`
	VerificationAfter          uint64               `json:"verification_after"`
	LastCheck                  time.Time            `json:"last_check"`
	LastSuccess                time.Time            `json:"last_success"`
	NextCheck                  time.Time            `json:"next_check"`
	LastLatency                time.Duration        `json:"last_latency"`
	LatencyAvailable           bool                 `json:"latency_available"`
	Warning                    bool                 `json:"warning"`
	LastOutcome                string               `json:"last_outcome,omitempty"`
	Actions                    map[string]Action    `json:"actions,omitempty"`
	Cooldowns                  map[string]time.Time `json:"cooldowns,omitempty"`
}

func (m Monitor) Clone() Monitor {
	m.IgnoreIncidents = append([]time.Time(nil), m.IgnoreIncidents...)
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
	Failure              FailureDiagnostic         `json:"failure,omitzero"`
	RetryDelay           time.Duration             `json:"retry_delay,omitzero"`
	ActionDeadline       time.Time                 `json:"action_deadline,omitzero"`
	AgentConnection      *AgentConnection          `json:"agent_connection,omitempty"`
	AgentObservation     *AgentObservationCommand  `json:"agent_observation,omitempty"`
	AgentReceipt         *AgentReceiptCommand      `json:"agent_receipt,omitempty"`
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
	Control                    *ControlNotice               `json:"control,omitempty"`
	FileIncarnation            string                       `json:"file_incarnation,omitempty"`
	Cause                      FailureDiagnostic            `json:"cause,omitzero"`
	Failure                    FailureDiagnostic            `json:"failure,omitzero"`
	DeliveryID                 string                       `json:"delivery_id,omitempty"`
	NotificationEpisode        string                       `json:"notification_episode,omitempty"`
	EndpointID                 string                       `json:"endpoint_id,omitempty"`
	RecoveryFailure            *RecoveryFailure             `json:"recovery_failure,omitempty"`
	AgentReceipt               *AgentReceipt                `json:"agent_receipt,omitempty"`
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
	if c.RetryDelay < 0 || c.RetryDelay > 24*time.Hour || c.RetryDelay != 0 && (c.Kind != "result" || !c.Retryable || c.Outcome == "success") {
		return ErrNotificationIdentity
	}
	if c.Failure != (FailureDiagnostic{}) {
		if (c.Kind != "pulse" && c.Kind != "result") || c.Outcome == "success" {
			return ErrFailureDiagnostic
		}
		if err := c.Failure.validate(commandMinimumFormat(c)); err != nil {
			return err
		}
	}
	if !c.ActionDeadline.IsZero() && (c.Kind != "start" || !c.ActionDeadline.After(c.At) || c.ActionDeadline.Sub(c.At) > 24*time.Hour) {
		return ErrNotificationIdentity
	}
	if c.Config != nil {
		for _, a := range c.Config.Actions {
			if a.RecoveryFailure != nil {
				return ErrRecoveryNotificationInvalid
			}
			if a.ExecutorKind == "agent" || a.AgentReceipt != nil || a.AgentConnection != nil {
				return ErrAgentActionInvalid
			}
		}
	}
	if handled, err := validateCommandExtensions(c); handled || err != nil {
		return err
	}
	if c.Kind == "agent_observation" {
		return validateAgentObservationCommand(c)
	}
	if c.AgentObservation != nil || c.AgentConnection != nil && (c.Kind != "start" || c.Driver != "a2a") {
		return ErrAgentActionInvalid
	}
	if c.Kind == "agent_receipt" {
		return validateAgentReceiptCommand(c)
	}
	if c.AgentReceipt != nil {
		return ErrAgentReceiptInvalid
	}
	if c.Kind == "start" && c.Driver == "a2a" {
		return validateAgentStart(c)
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
	if c.Kind == "local_action_review" {
		if !catalogIdentifier(c.ExecutorSession, 256) {
			return ErrExecutorUnfenced
		}
		c.ExecutorSession = ""
		return validateRecoveryReviewCommand(c)
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
		// Configuration carries policy before an operational lifecycle exists.
		// Validate it at the command's format; Apply assigns the stored version.
		config := *c.Config
		if config.LifecycleVersion == 0 && (config.Policy.AutoIgnore != nil || config.Policy.PeriodicMaintenance || config.Policy.RecoveryTimeout != 0) {
			config.LifecycleVersion = LifecycleFormatVersion
		}
		if err := validateNotificationIdentity(config, commandMinimumFormat(c)); err != nil {
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
