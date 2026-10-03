//go:build externaljobs

package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type compiledJobType struct{ parameters, results *jobSchema }

func jobTypeName(s string) bool {
	if len(s) == 0 || len(s) > 128 {
		return false
	}
	for i, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			continue
		}
		if i != 0 && strings.ContainsRune("._:-", r) {
			continue
		}
		return false
	}
	return true
}

func compileJobType(ctx context.Context, resource api.JobType) (compiledJobType, error) {
	var result compiledJobType
	if ctx == nil {
		return result, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if resource.APIVersion != api.APIVersion || resource.Kind != "JobType" || !jobTypeName(resource.Metadata.ID) ||
		!jobTypeName(resource.Spec.Version) || !jobTypeName(resource.Spec.Handler) || resource.Spec.ProtocolVersion != "1" {
		return result, ErrValidation
	}
	if len(resource.Metadata.UID) > 256 || len(resource.Metadata.ResourceVersion) > 256 || resource.Metadata.Generation < 0 || len(resource.Spec.Timeout) > 64 {
		return result, ErrValidation
	}
	if err := validateMetadata(resource.Metadata); err != nil {
		return result, ErrValidation
	}
	if resource.Spec.Kind != "check" && resource.Spec.Kind != "recovery" && resource.Spec.Kind != "notification" {
		return result, ErrValidation
	}
	if resource.Metadata.ID == "external" {
		return result, ErrValidation
	}
	for _, capability := range jobs.Capabilities() {
		if resource.Metadata.ID == capability.Driver {
			return result, ErrValidation
		}
	}
	if resource.Spec.Timeout != "" {
		d, err := time.ParseDuration(resource.Spec.Timeout)
		if err != nil || d <= 0 || d > 24*time.Hour {
			return result, ErrValidation
		}
	}
	if len(resource.Spec.RejectionCodes) > 32 {
		return result, ErrValidation
	}
	seen := make(map[string]bool, len(resource.Spec.RejectionCodes))
	for _, code := range resource.Spec.RejectionCodes {
		if !jobTypeName(code) || seen[code] {
			return result, ErrValidation
		}
		seen[code] = true
	}
	var err error
	result.parameters, err = compileJobSchema(ctx, resource.Spec.ParameterSchema)
	if err != nil {
		return compiledJobType{}, err
	}
	result.results, err = compileJobSchema(ctx, resource.Spec.ResultSchema)
	if err != nil {
		return compiledJobType{}, err
	}
	return result, nil
}

// PreparedJobType binds encrypted input to the captured revision and operator
// policy. It can be submitted once; an uncertain outcome requires an explicit read.
type PreparedJobType struct {
	catalog     *Catalog
	command     persistence.JobTypeCommand
	resource    api.JobType
	used        *atomic.Bool
	operationID *atomic.Pointer[string]
}

func (p *PreparedJobType) OperationID() string {
	if p == nil || p.operationID == nil {
		return ""
	}
	return operationIDValue(p.operationID)
}

func (PreparedJobType) String() string               { return "prepared job type (configuration omitted)" }
func (p PreparedJobType) Format(w fmt.State, _ rune) { _, _ = w.Write([]byte(p.String())) }
func (PreparedJobType) MarshalJSON() ([]byte, error) {
	return nil, errors.New("prepared job type cannot be serialized")
}

func (c *Catalog) checkJobTypeAuthority(ctx context.Context, expected persistence.OperatorAuthority) error {
	current, err := c.store.ObserveOperatorAuthority(ctx, expected.Actor, time.Now().UTC())
	if err != nil {
		return err
	}
	if current != expected {
		return persistence.ErrAuthenticationConflict
	}
	return nil
}

func (c *Catalog) openJobType(ctx context.Context, value persistence.JobTypeVersion) (api.JobType, error) {
	var resource api.JobType
	plain, err := c.sealer.Open(ctx, value.Record.Binding(c.storeID), value.Record.Payload)
	if err != nil {
		if ctx.Err() != nil {
			return resource, ctx.Err()
		}
		c.fail()
		return resource, ErrUnavailable
	}
	defer clear(plain)
	err = api.StrictDecode(plain, &resource)
	if err == nil {
		_, err = compileJobType(ctx, resource)
	}
	if err != nil {
		if ctx.Err() != nil {
			return api.JobType{}, ctx.Err()
		}
		c.fail()
		return api.JobType{}, ErrUnavailable
	}
	r := value.Record
	if r.Key.Kind != "JobType" || resource.Metadata.ID != r.Key.ID || resource.Metadata.UID != r.UID || resource.Metadata.ResourceVersion != r.Revision || resource.Metadata.Generation != int64(r.Generation) ||
		resource.Spec.Version != value.Version || resource.Spec.Kind != value.Category || resource.Spec.Handler != value.Handler || resource.Spec.ProtocolVersion != value.ProtocolVersion || value.SchemaProfile != jobSchemaProfile {
		c.fail()
		return api.JobType{}, ErrUnavailable
	}
	return resource, nil
}

func jobTypeSpecsEqual(ctx context.Context, a, b api.JobTypeSpec) (bool, error) {
	if a.Version != b.Version || a.Kind != b.Kind || a.Handler != b.Handler || a.ProtocolVersion != b.ProtocolVersion || a.Timeout != b.Timeout || !reflect.DeepEqual(a.RejectionCodes, b.RejectionCodes) {
		return false, nil
	}
	budget := &jobSchemaBudget{ctx: ctx}
	for _, pair := range [][2]json.RawMessage{{a.ParameterSchema, b.ParameterSchema}, {a.ResultSchema, b.ResultSchema}} {
		x, err := decodeJobJSON(ctx, pair[0], jobSchemaBytes)
		if err != nil {
			return false, err
		}
		y, err := decodeJobJSON(ctx, pair[1], jobSchemaBytes)
		if err != nil {
			return false, err
		}
		equal, err := jobEqual(x, y, budget)
		if err != nil || !equal {
			return equal, err
		}
	}
	return true, nil
}

// PrepareJobType validates both immutable contracts before encryption or any
// durable mutation. actor comes from authenticated admission, never request JSON.
func (c *Catalog) PrepareJobType(ctx context.Context, input api.JobType, expectedVersion string, create bool, actor string) (*PreparedJobType, error) {
	if err := c.readyContext(ctx); err != nil {
		return nil, err
	}
	at := time.Now().UTC()
	authority, err := c.store.ObserveOperatorAuthority(ctx, actor, at)
	if err != nil {
		return nil, err
	}
	// Bound raw schema fields before marshaling a caller-owned typed resource.
	if len(input.Spec.ParameterSchema) > jobSchemaBytes || len(input.Spec.ResultSchema) > jobSchemaBytes {
		return nil, ErrJobSchemaBudget
	}
	if _, err := compileJobType(ctx, input); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(input)
	if err != nil || len(raw) > api.MaxResourceBytes {
		return nil, ErrValidation
	}
	defer clear(raw)
	var resource api.JobType
	if err := api.StrictDecode(raw, &resource); err != nil {
		return nil, ErrValidation
	}
	if err := c.checkJobTypeAuthority(ctx, authority); err != nil {
		return nil, err
	}
	state, exists, err := c.store.JobType(ctx, resource.Metadata.ID)
	if err != nil {
		return nil, err
	}
	old := state.Current
	record := persistence.CatalogRecord{Key: persistence.CatalogKey{Kind: "JobType", ID: resource.Metadata.ID}, UID: uuid.NewString(), Revision: uuid.NewString(), Generation: 1, Purpose: "job-type", CreatedAt: at, UpdatedAt: at}
	command := persistence.JobTypeCommand{Action: "create", Authority: authority}
	if create {
		if expectedVersion != "" || resource.Metadata.UID != "" || resource.Metadata.ResourceVersion != "" || resource.Metadata.Generation != 0 {
			return nil, ErrValidation
		}
		if exists && !old.Record.Removed {
			return nil, persistence.ErrJobTypeConflict
		}
		if exists {
			if old.Category != resource.Spec.Kind {
				return nil, persistence.ErrJobTypeVersionConflict
			}
		}
	} else {
		if !exists || old.Record.Removed || expectedVersion == "" || old.Record.Revision != expectedVersion {
			return nil, persistence.ErrJobTypeConflict
		}
		if resource.Metadata.UID != "" && resource.Metadata.UID != old.Record.UID || resource.Metadata.ResourceVersion != "" && resource.Metadata.ResourceVersion != expectedVersion || resource.Metadata.Generation != 0 && uint64(resource.Metadata.Generation) != old.Record.Generation {
			return nil, persistence.ErrJobTypeConflict
		}
		if old.Category != resource.Spec.Kind {
			return nil, persistence.ErrJobTypeVersionConflict
		}
		command.Action = "replace"
		command.ExpectedUID, command.ExpectedRevision = old.Record.UID, old.Record.Revision
		record.UID, record.CreatedAt, record.Generation = old.Record.UID, old.Record.CreatedAt, old.Record.Generation+1
		if record.Generation == 0 {
			return nil, persistence.ErrJobTypeQuota
		}
	}
	if retained, ok := state.Versions[resource.Spec.Version]; ok {
		if create || old.Version != resource.Spec.Version {
			return nil, persistence.ErrJobTypeVersionConflict
		}
		if err := c.checkJobTypeAuthority(ctx, authority); err != nil {
			return nil, err
		}
		original, err := c.openJobType(ctx, retained)
		if err != nil {
			return nil, err
		}
		if err := c.checkJobTypeAuthority(ctx, authority); err != nil {
			return nil, err
		}
		equal, err := jobTypeSpecsEqual(ctx, resource.Spec, original.Spec)
		if err != nil {
			return nil, err
		}
		if !equal {
			return nil, persistence.ErrJobTypeVersionConflict
		}
		command.RetainedVersionRevision = retained.Record.Revision
	}
	resource.Metadata.UID, resource.Metadata.ResourceVersion, resource.Metadata.Generation = record.UID, record.Revision, int64(record.Generation)
	plain, err := json.Marshal(resource)
	if err != nil {
		return nil, ErrValidation
	}
	defer clear(plain)
	if err := c.checkJobTypeAuthority(ctx, authority); err != nil {
		return nil, err
	}
	record.Payload, err = c.sealer.Seal(ctx, record.Binding(c.storeID), plain)
	if err != nil {
		return nil, err
	}
	if err := c.checkJobTypeAuthority(ctx, authority); err != nil {
		return nil, err
	}
	command.Value = persistence.JobTypeVersion{Record: record, Version: resource.Spec.Version, Category: resource.Spec.Kind, Handler: resource.Spec.Handler, ProtocolVersion: resource.Spec.ProtocolVersion, SchemaProfile: jobSchemaProfile}
	return &PreparedJobType{catalog: c, command: command, resource: resource, used: &atomic.Bool{}, operationID: &atomic.Pointer[string]{}}, nil
}

func (c *Catalog) CommitJobType(ctx context.Context, prepared *PreparedJobType) (api.JobType, error) {
	result, err := c.CommitJobTypeMutation(ctx, prepared)
	return result.Resource, err
}

type JobTypeMutationResult struct {
	Resource       api.JobType
	Operation      api.Operation
	CommittedIndex uint64
}

func (c *Catalog) CommitJobTypeMutation(ctx context.Context, prepared *PreparedJobType) (JobTypeMutationResult, error) {
	if prepared == nil || prepared.catalog != c || prepared.used == nil || prepared.operationID == nil || !prepared.used.CompareAndSwap(false, true) {
		return JobTypeMutationResult{}, ErrValidation
	}
	if err := c.readyContext(ctx); err != nil {
		return JobTypeMutationResult{}, err
	}
	if err := c.checkJobTypeAuthority(ctx, prepared.command.Authority); err != nil {
		return JobTypeMutationResult{}, err
	}
	at := time.Now().UTC()
	prepared.command.Value.Record.UpdatedAt = at
	if prepared.command.Action == "create" {
		prepared.command.Value.Record.CreatedAt = at
	}
	reservation, err := c.store.ReserveJobTypeOperation(ctx, prepared.command, at)
	if err != nil {
		return JobTypeMutationResult{}, err
	}
	id := reservation.ID
	prepared.operationID.Store(&id)
	prepared.command.OperationID = id
	result, err := c.store.CommitJobTypeOperation(ctx, prepared.command, time.Now().UTC())
	if err != nil {
		if errors.Is(err, persistence.ErrCommitUnconfirmed) {
			return JobTypeMutationResult{}, errors.Join(ErrOutcomeUnconfirmed, err)
		}
		return JobTypeMutationResult{}, err
	}
	if result.State.Current.Record.Revision != prepared.command.Value.Record.Revision || result.Operation.ID != id {
		return JobTypeMutationResult{}, ErrOutcomeUnconfirmed
	}
	return JobTypeMutationResult{Resource: prepared.resource, Operation: operationView(result.Operation), CommittedIndex: result.State.Current.Record.CommittedIndex}, nil
}

func (c *Catalog) GetJobType(ctx context.Context, id string) (api.JobType, error) {
	if err := c.readyContext(ctx); err != nil {
		return api.JobType{}, err
	}
	state, ok, err := c.store.JobType(ctx, id)
	if err != nil {
		return api.JobType{}, err
	}
	if !ok || state.Current.Record.Removed {
		return api.JobType{}, persistence.ErrCatalogNotFound
	}
	return c.openJobType(ctx, state.Current)
}

func (c *Catalog) PrepareDeleteJobType(ctx context.Context, id, version, actor string) (*PreparedJobType, error) {
	if err := c.readyContext(ctx); err != nil {
		return nil, err
	}
	at := time.Now().UTC()
	authority, err := c.store.ObserveOperatorAuthority(ctx, actor, at)
	if err != nil {
		return nil, err
	}
	state, ok, err := c.store.JobType(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok || state.Current.Record.Removed || version == "" || state.Current.Record.Revision != version {
		return nil, persistence.ErrJobTypeConflict
	}
	value := state.Current
	if err := c.checkJobTypeAuthority(ctx, authority); err != nil {
		return nil, err
	}
	resource, err := c.openJobType(ctx, value)
	if err != nil {
		return nil, err
	}
	if err := c.checkJobTypeAuthority(ctx, authority); err != nil {
		return nil, err
	}
	old := value.Record
	value.Record.Revision, value.Record.UpdatedAt = uuid.NewString(), at
	value.Record.Generation++
	if value.Record.Generation == 0 {
		return nil, persistence.ErrJobTypeQuota
	}
	value.Record.Removed, value.Record.Payload = true, secureconfig.Envelope{}
	value.Record.CommittedIndex, value.Record.DependentsVersion = 0, 0
	resource.Metadata.ResourceVersion, resource.Metadata.Generation = value.Record.Revision, int64(value.Record.Generation)
	return &PreparedJobType{catalog: c, resource: resource, used: &atomic.Bool{}, operationID: &atomic.Pointer[string]{}, command: persistence.JobTypeCommand{Action: "delete", Value: value, ExpectedUID: old.UID, ExpectedRevision: old.Revision, Authority: authority}}, nil
}

func (c *Catalog) verifyJobTypes(ctx context.Context) error {
	ids, err := c.store.JobTypeIDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		state, ok, err := c.store.JobType(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			c.fail()
			return ErrUnavailable
		}
		var retainedCurrent api.JobType
		for version, value := range state.Versions {
			resource, err := c.openJobType(ctx, value)
			if err != nil {
				return err
			}
			if version == state.Current.Version {
				retainedCurrent = resource
			}
		}
		if !state.Current.Record.Removed {
			current, err := c.openJobType(ctx, state.Current)
			if err != nil {
				return err
			}
			equal, err := jobTypeSpecsEqual(ctx, current.Spec, retainedCurrent.Spec)
			if err != nil {
				return err
			}
			if !equal {
				c.fail()
				return ErrUnavailable
			}
		}
	}
	return ctx.Err()
}
