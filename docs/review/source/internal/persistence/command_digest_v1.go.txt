package persistence

import (
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/internal/slo"
	"time"
)

// These wire shapes are frozen for existing replay/reservation digests. Their
// explicit projection and field-coverage test reject unreviewed shape changes.
// New digest formats must preserve these projections for retained identities.
type operationCommandDigestV1 struct {
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
	Catalog              *catalogMutationDigestV1  `json:"catalog,omitempty"`
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

type authenticationCommandDigestV1 struct {
	// Zero is the exact historical command format. New API submissions select
	// the current lifecycle format before serialization and digest calculation.
	Version           int                       `json:"version,omitempty"`
	Mode              string                    `json:"mode"`
	ExpectedEpoch     string                    `json:"expected_epoch,omitempty"`
	ExpectedRevision  string                    `json:"expected_revision,omitempty"`
	Epoch             string                    `json:"epoch"`
	Revision          string                    `json:"revision"`
	Actor             string                    `json:"actor"`
	At                time.Time                 `json:"at"`
	AnonymousLoopback bool                      `json:"anonymous_loopback,omitempty"`
	Principals        []AuthenticationPrincipal `json:"principals,omitempty"`
	LegacyTokenSHA256 string                    `json:"legacy_token_sha256,omitempty"`
}

// legacyOperationCommandDigest excludes optional extension fields only after
// operationDigest has rejected their presence. Keep the historical field order
// and wire tags above intact; new commands require their own digest contract.
func legacyOperationCommandDigest(c Command) operationCommandDigestV1 {
	return operationCommandDigestV1{
		CollectionExecute: c.CollectionExecute, Collection: c.Collection,
		OperationAllocation: c.OperationAllocation, Authentication: c.Authentication,
		Restore: c.Restore, ManualRecovery: c.ManualRecovery, ActionReview: c.ActionReview,
		ExecutorSession: c.ExecutorSession, Control: c.Control,
		CheckControlRevision: c.CheckControlRevision, Bootstrap: c.Bootstrap,
		Guard: c.Guard, Operation: c.Operation, Catalog: legacyCatalogMutationDigest(c.Catalog), SLO: c.SLO,
		Kind: c.Kind, MonitorID: c.MonitorID, Revision: c.Revision, At: c.At,
		Config: c.Config, Generation: c.Generation, ActionID: c.ActionID,
		Outcome: c.Outcome, Retryable: c.Retryable, Ambiguous: c.Ambiguous,
		Maintenance: c.Maintenance, Warning: c.Warning, Scheduled: c.Scheduled,
		ExecutionStart: c.ExecutionStart, ExecutionEnd: c.ExecutionEnd,
		Missed: c.Missed, Driver: c.Driver,
	}
}

// Catalog references introduced in format18 use a separate operation digest.
// Explicit historical fields keep an omitted tagged extension from silently
// changing existing operation identities as the live record grows.
type catalogRecordDigestV1 struct {
	Key               CatalogKey            `json:"key"`
	UID               string                `json:"uid"`
	Revision          string                `json:"revision"`
	Generation        uint64                `json:"generation"`
	Purpose           string                `json:"purpose"`
	Payload           secureconfig.Envelope `json:"payload"`
	References        []CatalogKey          `json:"references,omitempty"`
	CreatedAt         time.Time             `json:"created_at"`
	UpdatedAt         time.Time             `json:"updated_at"`
	Removed           bool                  `json:"removed,omitempty"`
	CommittedIndex    uint64                `json:"committed_index"`
	DependentsVersion uint64                `json:"dependents_version"`
}
type catalogMutationDigestV1 struct {
	OperationID               string                `json:"operation_id,omitempty"`
	Actor                     string                `json:"actor,omitempty"`
	Record                    catalogRecordDigestV1 `json:"record"`
	Create                    bool                  `json:"create,omitempty"`
	ExpectedUID               string                `json:"expected_uid,omitempty"`
	ExpectedRevision          string                `json:"expected_revision,omitempty"`
	ExpectedDependentsVersion uint64                `json:"expected_dependents_version"`
	Conditions                []CatalogCondition    `json:"conditions,omitempty"`
}

func legacyCatalogMutationDigest(m *CatalogMutation) *catalogMutationDigestV1 {
	if m == nil {
		return nil
	}
	r := m.Record
	return &catalogMutationDigestV1{OperationID: m.OperationID, Actor: m.Actor, Create: m.Create, ExpectedUID: m.ExpectedUID, ExpectedRevision: m.ExpectedRevision, ExpectedDependentsVersion: m.ExpectedDependentsVersion, Conditions: m.Conditions, Record: catalogRecordDigestV1{Key: r.Key, UID: r.UID, Revision: r.Revision, Generation: r.Generation, Purpose: r.Purpose, Payload: r.Payload, References: r.References, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, Removed: r.Removed, CommittedIndex: r.CommittedIndex, DependentsVersion: r.DependentsVersion}}
}
