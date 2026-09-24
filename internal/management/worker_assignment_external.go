//go:build externaljobs

package management

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type workerAssignmentPayloadV1 struct {
	Identity          string          `json:"identity"`
	Parameters        json.RawMessage `json:"parameters"`
	CredentialProfile string          `json:"credential_profile"`
}

// Freeze the authenticated projection independently of future intent fields.
func workerAssignmentIdentity(in persistence.WorkerExecutionIntent) (string, error) {
	value := struct {
		ID, Revision, MonitorID, MonitorUID, MonitorRevision, ControlRevision string
		Category, ActionID                                                    string
		Generation                                                            uint64
		Source                                                                persistence.CatalogKey
		SourceUID, SourceRevision                                             string
		JobType                                                               persistence.JobTypeReference
		Guard                                                                 persistence.CatalogGuard
		Scheduled, Deadline                                                   time.Time
	}{in.ID, in.Revision, in.MonitorID, in.MonitorUID, in.MonitorRevision, in.ControlRevision,
		in.Category, in.ActionID, in.Generation, in.Source, in.SourceUID, in.SourceRevision,
		in.JobType, in.Guard, in.Scheduled, in.Deadline}
	h := sha256.New()
	_, _ = h.Write([]byte("cpra/worker/assignment-identity/v1\x00"))
	if err := json.NewEncoder(h).Encode(value); err != nil {
		return "", persistence.ErrWorkerExecutionInvalid
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// PrepareWorkerExecution reads parameters from authenticated configuration. The
// owner supplies scheduling identities; workers cannot submit these intents.
// CommitWorkerExecution must recheck controls and dependencies after preparation.
func (c *Catalog) PrepareWorkerExecution(ctx context.Context, intent persistence.WorkerExecutionIntent) (persistence.WorkerExecutionIntent, error) {
	fail := func(err error) (persistence.WorkerExecutionIntent, error) {
		return persistence.WorkerExecutionIntent{}, err
	}
	if ctx == nil {
		return fail(persistence.ErrWorkerExecutionInvalid)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if intent.Payload.Format != 0 || intent.Payload.KeyID != "" || len(intent.Payload.WrappedKey)+len(intent.Payload.Nonce)+len(intent.Payload.Ciphertext) != 0 ||
		intent.JobType.Validate() != nil || intent.Category != intent.JobType.Category || len(intent.Guard.Conditions) < 1 || len(intent.Guard.Conditions) > 10000 ||
		intent.Scheduled.IsZero() || intent.Scheduled.Year() < 1 || !intent.Deadline.After(intent.Scheduled) || intent.Deadline.Sub(intent.Scheduled) > 24*time.Hour {
		return fail(persistence.ErrWorkerExecutionInvalid)
	}
	for _, id := range []string{intent.ID, intent.Revision, intent.MonitorID, intent.MonitorUID, intent.MonitorRevision, intent.ControlRevision, intent.Source.ID, intent.SourceUID, intent.SourceRevision} {
		if id == "" || !workerOutcomeText(id, 256) {
			return fail(persistence.ErrWorkerExecutionInvalid)
		}
	}
	for _, condition := range intent.Guard.Conditions {
		if condition.Key.ID == "" || !workerOutcomeText(condition.Key.ID, 256) || len(condition.Key.Kind) > 64 || condition.UID == "" || !workerOutcomeText(condition.UID, 256) || condition.Revision == "" || !workerOutcomeText(condition.Revision, 256) {
			return fail(persistence.ErrWorkerExecutionInvalid)
		}
	}
	slot := map[string]string{"check": "check", "recovery": "recovery", "notification": "endpoint"}[intent.Category]
	if slot == "" || intent.Category == "notification" && intent.Source.Kind != "NotificationEndpoint" || intent.Category != "notification" && (intent.Source != (persistence.CatalogKey{Kind: "Monitor", ID: intent.MonitorID}) || intent.SourceUID != intent.MonitorUID) {
		return fail(persistence.ErrWorkerExecutionInvalid)
	}
	if err := c.readyContext(ctx); err != nil {
		return fail(err)
	}
	intent = intent.Clone()
	view, err := c.store.CatalogSnapshotContext(ctx)
	if err != nil {
		return fail(workerPreparationError(ctx, err))
	}
	record, ok := view.Get(intent.Source)
	if !ok || record.UID != intent.SourceUID || record.Revision != intent.SourceRevision {
		return fail(persistence.ErrCatalogDependency)
	}
	resource, err := c.open(ctx, record)
	if err != nil {
		return fail(err)
	}
	bindings, err := externalRuntimeBindings(ctx, resource, record)
	if err != nil {
		return fail(err)
	}
	want := manifest.ExternalJobIdentity{JobTypeID: intent.JobType.JobTypeID, JobTypeUID: intent.JobType.JobTypeUID, Version: intent.JobType.Version, Revision: intent.JobType.Revision, Category: intent.JobType.Category}
	var descriptor manifest.ExternalJobDescriptor
	found := false
	for _, binding := range bindings {
		if binding.Key.Slot == slot && binding.Key.JobType == want {
			descriptor, found = binding.Descriptor, true
			break
		}
	}
	if !found {
		return fail(persistence.ErrCatalogDependency)
	}
	contract, err := c.workerExecutionContract(ctx, intent.JobType)
	if err != nil {
		return fail(err)
	}
	if contract.timeout > 0 && intent.Deadline.Sub(intent.Scheduled) > contract.timeout {
		return fail(persistence.ErrWorkerExecutionInvalid)
	}
	identity, err := workerAssignmentIdentity(intent)
	if err != nil {
		return fail(err)
	}
	parameters := descriptor.Parameters()
	defer clear(parameters)
	plain, err := json.Marshal(workerAssignmentPayloadV1{Identity: identity, Parameters: parameters, CredentialProfile: descriptor.CredentialProfile()})
	if err != nil || len(plain) > persistence.MaxWorkerExecutionPayload {
		clear(plain)
		return fail(persistence.ErrWorkerExecutionQuota)
	}
	defer clear(plain)
	intent.Payload, err = c.sealer.Seal(ctx, intent.Binding(c.storeID), plain)
	if err != nil {
		return fail(workerPreparationError(ctx, err))
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if err := intent.Validate(); err != nil {
		return fail(err)
	}
	return intent, nil
}

type workerExecutionContract struct {
	parameters *jobSchema
	timeout    time.Duration
}

func (c *Catalog) workerExecutionContract(ctx context.Context, pin persistence.JobTypeReference) (workerExecutionContract, error) {
	selected, ok, err := c.store.LookupJobTypeVersion(ctx, pin.JobTypeID, pin.Version)
	if err != nil {
		return workerExecutionContract{}, workerPreparationError(ctx, err)
	}
	if !ok || selected.Version.Record.UID != pin.JobTypeUID || selected.Version.Record.Revision != pin.Revision || selected.Version.Category != pin.Category {
		return workerExecutionContract{}, persistence.ErrCatalogDependency
	}
	resource, err := c.openJobType(ctx, selected.Version)
	if err != nil {
		return workerExecutionContract{}, err
	}
	compiled, err := compileJobType(ctx, resource)
	if err != nil {
		return workerExecutionContract{}, err
	}
	timeout, _ := time.ParseDuration(resource.Spec.Timeout)
	return workerExecutionContract{parameters: compiled.parameters, timeout: timeout}, nil
}

// OpenWorkerExecution authenticates the original sealed assignment, including
// its entire scheduling/dependency identity. This is not a start grant.
func (c *Catalog) OpenWorkerExecution(ctx context.Context, record persistence.WorkerExecutionRecord) (manifest.ExternalJobDescriptor, error) {
	if ctx == nil || record.Intent.Validate() != nil {
		return manifest.ExternalJobDescriptor{}, persistence.ErrWorkerExecutionInvalid
	}
	if err := c.readyContext(ctx); err != nil {
		return manifest.ExternalJobDescriptor{}, err
	}
	return c.openWorkerExecution(ctx, record)
}

// The startup verifier uses the same authenticated decoder before admission.
func (c *Catalog) openWorkerExecution(ctx context.Context, record persistence.WorkerExecutionRecord) (manifest.ExternalJobDescriptor, error) {
	if ctx == nil || record.Intent.Validate() != nil {
		return manifest.ExternalJobDescriptor{}, persistence.ErrWorkerExecutionInvalid
	}
	if err := ctx.Err(); err != nil {
		return manifest.ExternalJobDescriptor{}, err
	}
	in := record.Intent
	plain, err := c.sealer.Open(ctx, in.Binding(c.storeID), in.Payload)
	if err != nil {
		if ctx.Err() != nil {
			return manifest.ExternalJobDescriptor{}, ctx.Err()
		}
		c.fail()
		return manifest.ExternalJobDescriptor{}, ErrUnavailable
	}
	defer clear(plain)
	var payload workerAssignmentPayloadV1
	defer func() { clear(payload.Parameters) }()
	if len(plain) > persistence.MaxWorkerExecutionPayload || api.StrictDecode(plain, &payload) != nil {
		c.fail()
		return manifest.ExternalJobDescriptor{}, ErrUnavailable
	}
	identity, err := workerAssignmentIdentity(in)
	if err != nil || payload.Identity != identity {
		c.fail()
		return manifest.ExternalJobDescriptor{}, ErrUnavailable
	}
	contract, err := c.workerExecutionContract(ctx, in.JobType)
	if err != nil {
		return manifest.ExternalJobDescriptor{}, err
	}
	if err := contract.parameters.validate(ctx, payload.Parameters); err != nil {
		if ctx.Err() != nil {
			return manifest.ExternalJobDescriptor{}, ctx.Err()
		}
		c.fail()
		return manifest.ExternalJobDescriptor{}, ErrUnavailable
	}
	id := manifest.ExternalJobIdentity{JobTypeID: in.JobType.JobTypeID, JobTypeUID: in.JobType.JobTypeUID, Version: in.JobType.Version, Revision: in.JobType.Revision, Category: in.JobType.Category}
	descriptor, err := manifest.NewExternalJobDescriptor(id, payload.Parameters, payload.CredentialProfile)
	if err != nil {
		c.fail()
		return manifest.ExternalJobDescriptor{}, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return manifest.ExternalJobDescriptor{}, err
	}
	return descriptor, nil
}

func workerPreparationError(ctx context.Context, _ error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return ErrUnavailable
}
