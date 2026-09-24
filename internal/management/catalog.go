package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

var (
	ErrValidation         = errors.New("invalid resource")
	ErrUnavailable        = errors.New("management catalog unavailable")
	ErrOutcomeUnconfirmed = errors.New("mutation outcome is unconfirmed; reconcile the original resource revision")
	ErrGraphLimit         = errors.New("affected resource graph exceeds synchronous validation limit; use a collection operation")
)

const maxValidationGraph = persistence.CollectionValidationMaxItems

// Catalog owns encrypted resource preparation and conditional commits. It never
// touches Ark or performs provider I/O. A successful commit reports durable state;
// owner-loop reconciliation separately establishes observedGeneration.
type Catalog struct {
	store    *persistence.Store
	sealer   *secureconfig.Sealer
	storeID  string
	failed   atomic.Bool
	verified atomic.Bool
	// Only inactive cancellation is serialized here, before policy admission.
	// No key wrapping, provider execution or upload work holds this semaphore.
	collectionCancel chan struct{}
	// One coalesced wakeup; durable headers are the queue. Shutdown admission
	// remains closed for this Catalog once its process-owned worker stops.
	validationWake     chan struct{}
	validationStopping atomic.Bool
}

func NewCatalog(store *persistence.Store, sealer *secureconfig.Sealer) (*Catalog, error) {
	if store == nil || sealer == nil || store.Status().NodeID == "" {
		return nil, errors.New("catalog requires an initialized store and encryption keyring")
	}
	return &Catalog{store: store, sealer: sealer, storeID: store.Status().NodeID, collectionCancel: make(chan struct{}, 1), validationWake: make(chan struct{}, 1)}, nil
}

// Verify authenticates retained configuration and execution payloads before
// opening admission. This streaming startup check is not repeated by API reads.
func (c *Catalog) Verify(ctx context.Context) error {
	if ctx == nil {
		return ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	view, err := c.store.CatalogSnapshotContext(ctx)
	if err != nil {
		return errors.Join(ErrUnavailable, err)
	}
	verified := 0
	for _, kind := range ResourceKinds() {
		after := ""
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			records, next, err := view.Page(kind, after, 100)
			if err != nil {
				return ErrUnavailable
			}
			for _, record := range records {
				if _, err := c.open(ctx, record); err != nil {
					return err
				}
				verified++
			}
			if next == "" {
				break
			}
			after = next
		}
	}
	if verified != view.Len() {
		c.fail()
		return ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.verifyJobTypes(ctx); err != nil {
		return err
	}
	if err := c.verifyWorkerExecutions(ctx); err != nil {
		return err
	}
	c.verified.Store(true)
	return nil
}

func ResourceKinds() []string {
	return []string{"Credential", "NotificationEndpoint", "Recipient", "NotificationGroup", "Monitor"}
}

func (c *Catalog) Ready() bool {
	return c.verified.Load() && !c.failed.Load() && c.store.Status().Ready
}

// readyContext avoids the uncancellable diagnostic Status path for owner work.
// Cancellation and leadership loss are observations, never catalog corruption.
func (c *Catalog) readyContext(ctx context.Context) error {
	if ctx == nil {
		return ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil || c.store == nil || !c.verified.Load() || c.failed.Load() {
		return ErrUnavailable
	}
	if err := c.store.ControllerHealthContext(ctx); err != nil {
		return errors.Join(ErrUnavailable, err)
	}
	return nil
}

func (c *Catalog) fail() {
	c.failed.Store(true)
	c.store.MarkUnavailable(ErrUnavailable)
}

func (c *Catalog) open(ctx context.Context, record persistence.CatalogRecord) (api.Resource, error) {
	var resource api.Resource
	plain, err := c.sealer.Open(ctx, record.Binding(c.storeID), record.Payload)
	if err != nil {
		if ctx.Err() != nil {
			return resource, ctx.Err()
		}
		c.fail()
		return resource, ErrUnavailable
	}
	defer clear(plain)
	if api.StrictDecode(plain, &resource) != nil || !supportedKind(resource.Kind) ||
		api.ValidateResource(resource) != nil || resource.Kind != record.Key.Kind ||
		resource.Metadata.ID != record.Key.ID || resource.Metadata.UID != record.UID ||
		resource.Metadata.ResourceVersion != record.Revision || resource.Metadata.Generation != int64(record.Generation) {
		c.fail()
		return api.Resource{}, ErrUnavailable
	}
	// Reference edges are cleartext indexes. Authenticate their meaning against
	// the encrypted resource before trusting them for shared-resource validation.
	refs, err := directReferences(resource)
	if err != nil || len(refs) != len(record.References) {
		c.fail()
		return api.Resource{}, ErrUnavailable
	}
	edges := make(map[persistence.CatalogKey]bool, len(record.References))
	for _, ref := range record.References {
		edges[ref] = true
	}
	for _, ref := range refs {
		if !edges[ref] {
			c.fail()
			return api.Resource{}, ErrUnavailable
		}
	}
	if err := c.authenticateJobTypeReferences(ctx, resource, record); err != nil {
		return api.Resource{}, err
	}
	return resource, nil
}

func publicResource(resource api.Resource) api.Resource {
	if resource.Kind == "Credential" {
		var spec api.CredentialSpec
		_ = json.Unmarshal(resource.Spec, &spec)
		available := spec.Value != nil
		spec.Value = nil
		resource.Spec, _ = json.Marshal(spec)
		resource.Status, _ = json.Marshal(struct {
			Available bool `json:"available"`
		}{available})
	}
	return resource
}

func (c *Catalog) Get(ctx context.Context, kind, id string) (api.Resource, error) {
	if !supportedKind(kind) || !validID(id) {
		return api.Resource{}, ErrValidation
	}
	if !c.Ready() {
		return api.Resource{}, ErrUnavailable
	}
	record, ok, err := c.store.CatalogGet(persistence.CatalogKey{Kind: kind, ID: id})
	if err != nil {
		return api.Resource{}, ErrUnavailable
	}
	if !ok {
		return api.Resource{}, persistence.ErrCatalogNotFound
	}
	resource, err := c.open(ctx, record)
	if err != nil {
		return api.Resource{}, err
	}
	return c.observedResource(ctx, resource)
}

// ReadView retains an immutable encrypted catalog generation. The HTTP cursor
// layer owns principal binding, TTL and retained-view quotas.
type ReadView struct {
	view    persistence.CatalogView
	catalog *Catalog
}

func (c *Catalog) Snapshot() (ReadView, error) {
	return c.SnapshotContext(context.Background())
}

// SnapshotContext retains a catalog generation with cancellable readiness and
// owner-lock waits. The returned view owns no storage lock.
func (c *Catalog) SnapshotContext(ctx context.Context) (ReadView, error) {
	if err := c.readyContext(ctx); err != nil {
		return ReadView{}, err
	}
	v, err := c.store.CatalogSnapshotContext(ctx)
	if err != nil {
		return ReadView{}, err
	}
	return ReadView{view: v, catalog: c}, nil
}

func (v ReadView) Index() uint64 { return v.view.Index }

// ChangeCursor starts incremental reconciliation after this exact frozen view.
// It is process-local; API pagination continues to use its own authenticated cursor.
func (v ReadView) ChangeCursor() persistence.CatalogCursor { return v.view.Cursor }

func (v ReadView) Page(ctx context.Context, kind, after string, limit int) ([]api.Resource, string, error) {
	if !supportedKind(kind) || v.catalog == nil {
		return nil, "", ErrUnavailable
	}
	if err := v.catalog.readyContext(ctx); err != nil {
		return nil, "", err
	}
	records, next, err := v.view.Page(kind, after, limit)
	if err != nil {
		return nil, "", err
	}
	resources := make([]api.Resource, 0, len(records))
	for _, record := range records {
		resource, err := v.catalog.open(ctx, record)
		if err != nil {
			return nil, "", err
		}
		resource, err = v.catalog.observedResource(ctx, resource)
		if err != nil {
			return nil, "", err
		}
		resources = append(resources, resource)
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	return resources, next, nil
}

func supportedKind(kind string) bool {
	switch kind {
	case "Monitor", "Credential", "NotificationEndpoint", "Recipient", "NotificationGroup":
		return true
	}
	return false
}

// PreparedChange contains ciphertext and a safe read representation only. It is
// bound to the preparing catalog and all versions observed during validation.
// Commit it once; an uncertain response must be reconciled, never replayed.
type PreparedChange struct {
	catalog     *Catalog
	mutation    persistence.CatalogMutation
	resource    api.Resource
	operationID atomic.Pointer[string]
	used        atomic.Bool
}

type MutationResult struct {
	Resource       api.Resource
	CommittedIndex uint64
	Operation      api.Operation
}

// OperationID is assigned by confirmed reservation inside admission, before the
// target mutation. It is empty after pure preparation or unconfirmed allocation,
// and retained after target uncertainty. It contains no configuration value.
func (p *PreparedChange) OperationID() string {
	if p == nil {
		return ""
	}
	return operationIDValue(&p.operationID)
}

// Prepare prepares create/replace without provider execution or durable writes.
// expectedVersion is empty only for create-if-absent.
func (c *Catalog) Prepare(ctx context.Context, input api.Resource, expectedVersion string, create bool) (*PreparedChange, error) {
	if !c.Ready() {
		return nil, ErrUnavailable
	}
	raw, err := json.Marshal(input)
	if err != nil || len(raw) > api.MaxResourceBytes {
		return nil, ErrValidation
	}
	resource, err := api.DecodeResource(raw)
	if err != nil || !supportedKind(resource.Kind) || !validID(resource.Metadata.ID) {
		return nil, ErrValidation
	}
	if err := validateMetadata(resource.Metadata); err != nil {
		return nil, err
	}
	if resource.Kind == "Monitor" {
		if _, err := (manifest.Monitor{ID: resource.Metadata.ID}).EffectiveID(); err != nil {
			return nil, fmt.Errorf("%w: monitor id must start with a letter or digit and contain 1..128 letters, digits, dot, underscore, colon or hyphen", ErrValidation)
		}
	}
	key := persistence.CatalogKey{Kind: resource.Kind, ID: resource.Metadata.ID}
	view, err := c.store.CatalogSnapshot()
	if err != nil {
		return nil, ErrUnavailable
	}
	old, exists := view.Get(key)
	if create {
		if exists {
			return nil, persistence.ErrCatalogConflict
		}
		if expectedVersion != "" || resource.Metadata.UID != "" || resource.Metadata.ResourceVersion != "" || resource.Metadata.Generation != 0 {
			return nil, fmt.Errorf("%w: create cannot assign server-owned metadata", ErrValidation)
		}
	} else {
		if !exists {
			return nil, persistence.ErrCatalogNotFound
		}
		if expectedVersion == "" || old.Revision != expectedVersion ||
			(resource.Metadata.ResourceVersion != "" && resource.Metadata.ResourceVersion != expectedVersion) ||
			(resource.Metadata.UID != "" && resource.Metadata.UID != old.UID) ||
			(resource.Metadata.Generation != 0 && uint64(resource.Metadata.Generation) != old.Generation) {
			return nil, persistence.ErrCatalogConflict
		}
	}
	resource.Status = nil
	now := time.Now().UTC()
	record := persistence.CatalogRecord{Key: key, UID: uuid.NewString(), Revision: uuid.NewString(),
		Generation: 1, Purpose: "desired-resource", CreatedAt: now, UpdatedAt: now}
	var previous api.Resource
	if !create {
		previous, err = c.open(ctx, old)
		if err != nil {
			return nil, err
		}
		record.UID, record.CreatedAt, record.Generation = old.UID, old.CreatedAt, old.Generation
		if resource.Kind == "Credential" {
			if err := preserveCredential(&resource, previous); err != nil {
				return nil, err
			}
		}
		if !jsonEqual(resource.Spec, previous.Spec) {
			record.Generation++
		}
	}
	resource.Metadata.UID, resource.Metadata.ResourceVersion = record.UID, record.Revision
	resource.Metadata.Generation = int64(record.Generation)
	if err := validateDesired(&resource); err != nil {
		return nil, err
	}
	refs, conditions, err := c.validateGraph(ctx, view, resource, old, create)
	if err != nil {
		return nil, err
	}
	record.References = refs
	if err := c.prepareJobTypeReferences(ctx, resource, &record); err != nil {
		return nil, err
	}
	plain, err := json.Marshal(resource)
	if err != nil {
		return nil, ErrValidation
	}
	defer clear(plain)
	record.Payload, err = c.sealer.Seal(ctx, record.Binding(c.storeID), plain)
	if err != nil {
		return nil, err
	}
	mutation := persistence.CatalogMutation{Record: record, Create: create, Conditions: conditions, OperationID: record.Revision, Actor: "local"}
	if !create {
		mutation.ExpectedUID, mutation.ExpectedRevision = old.UID, old.Revision
		mutation.ExpectedDependentsVersion = old.DependentsVersion
	}
	return &PreparedChange{catalog: c, mutation: mutation, resource: publicResource(resource)}, nil
}

func (c *Catalog) PrepareDelete(ctx context.Context, kind, id, expectedVersion string) (*PreparedChange, error) {
	if !supportedKind(kind) || !validID(id) || expectedVersion == "" {
		return nil, ErrValidation
	}
	if !c.Ready() {
		return nil, ErrUnavailable
	}
	old, ok, err := c.store.CatalogGet(persistence.CatalogKey{Kind: kind, ID: id})
	if err != nil {
		return nil, ErrUnavailable
	}
	if !ok {
		return nil, persistence.ErrCatalogNotFound
	}
	if old.Revision != expectedVersion {
		return nil, persistence.ErrCatalogConflict
	}
	resource, err := c.open(ctx, old)
	if err != nil {
		return nil, err
	}
	record := old.Clone()
	record.Revision, record.UpdatedAt = uuid.NewString(), time.Now().UTC()
	record.Removed, record.Payload, record.References = true, secureconfig.Envelope{}, nil
	clearJobTypeReferences(&record)
	record.CommittedIndex, record.DependentsVersion = 0, 0
	resource.Metadata.ResourceVersion = record.Revision
	return &PreparedChange{catalog: c, resource: publicResource(resource), mutation: persistence.CatalogMutation{
		Record: record, ExpectedUID: old.UID, ExpectedRevision: old.Revision,
		ExpectedDependentsVersion: old.DependentsVersion, OperationID: record.Revision, Actor: "local"}}, nil
}

func (c *Catalog) Commit(ctx context.Context, prepared *PreparedChange) (MutationResult, error) {
	return c.CommitAs(ctx, prepared, "local")
}

// CommitAs receives the authenticated actor from the admission boundary. HTTP
// handlers must never take this identity from submitted resource data.
func (c *Catalog) CommitAs(ctx context.Context, prepared *PreparedChange, actor string) (MutationResult, error) {
	if prepared == nil || prepared.catalog != c || !prepared.used.CompareAndSwap(false, true) {
		return MutationResult{}, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return MutationResult{}, err
	}
	if !c.Ready() {
		return MutationResult{}, ErrUnavailable
	}
	if actor == "" || len(actor) > 128 || !utf8.ValidString(actor) || strings.ContainsAny(actor, "\x00\r\n") {
		return MutationResult{}, ErrValidation
	}
	prepared.mutation.Actor = actor
	command := persistence.Command{Kind: "catalog", Catalog: &prepared.mutation}
	id, err := c.reserveCommand(ctx, &command)
	if err != nil {
		return MutationResult{}, err
	}
	prepared.operationID.Store(&id)
	results, err := c.store.Submit(ctx, []persistence.Command{command})
	if err != nil {
		if errors.Is(err, persistence.ErrCommitUnconfirmed) {
			return MutationResult{}, fmt.Errorf("%w: %w", ErrOutcomeUnconfirmed, err)
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return MutationResult{}, err
		}
		if !c.store.Status().Ready {
			return MutationResult{}, ErrUnavailable
		}
		return MutationResult{}, err
	}
	if len(results) != 1 {
		return MutationResult{}, ErrOutcomeUnconfirmed
	}
	if results[0].Err != nil {
		return MutationResult{}, results[0].Err
	}
	if !results[0].Allowed || results[0].Catalog == nil || results[0].Operation == nil {
		return MutationResult{}, ErrOutcomeUnconfirmed
	}
	return MutationResult{Resource: prepared.resource, CommittedIndex: results[0].Catalog.CommittedIndex, Operation: operationView(*results[0].Operation)}, nil
}

func validateMetadata(m api.Metadata) error {
	if m.Name != nil && len(*m.Name) > 256 {
		return ErrValidation
	}
	if m.Labels != nil {
		if len(*m.Labels) > 64 {
			return ErrValidation
		}
		for key, value := range *m.Labels {
			if key == "" || len(key) > 128 || len(value) > 256 {
				return ErrValidation
			}
		}
	}
	return nil
}

func validateDesired(resource *api.Resource) error {
	if resource.Kind == "Credential" {
		var spec api.CredentialSpec
		if api.StrictDecode(resource.Spec, &spec) != nil || spec.Value == nil ||
			*spec.Value == "" || *spec.Value == "[REDACTED]" || *spec.Value == "<redacted>" || *spec.Value == "********" {
			return fmt.Errorf("%w: supply a credential value; redaction placeholders are not values", ErrValidation)
		}
	}
	return visitDrivers(resource, func(category string, driver *api.DriverConfig) error {
		if err := jobs.ValidateDriver(category, driver.Type); err != nil {
			return fmt.Errorf("%w: selected driver is not compiled into this server", ErrValidation)
		}
		if err := validateProtectedDriver(category, *driver); err != nil {
			return fmt.Errorf("%w: %w", ErrValidation, err)
		}
		return nil
	})
}

func preserveCredential(resource *api.Resource, previous api.Resource) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(resource.Spec, &fields) != nil {
		return ErrValidation
	}
	if raw, ok := fields["value"]; ok && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ErrValidation
	}
	var spec, old api.CredentialSpec
	if api.StrictDecode(resource.Spec, &spec) != nil || api.StrictDecode(previous.Spec, &old) != nil {
		return ErrValidation
	}
	if spec.Value == nil {
		spec.Value = old.Value
	}
	resource.Spec, _ = json.Marshal(spec)
	return nil
}

func jsonEqual(a, b []byte) bool {
	var va, vb any
	da, db := json.NewDecoder(bytes.NewReader(a)), json.NewDecoder(bytes.NewReader(b))
	da.UseNumber()
	db.UseNumber()
	if da.Decode(&va) != nil || db.Decode(&vb) != nil {
		return false
	}
	aa, _ := json.Marshal(va)
	bb, _ := json.Marshal(vb)
	return bytes.Equal(aa, bb)
}
