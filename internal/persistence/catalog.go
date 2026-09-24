package persistence

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/btree"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

// CatalogFormatVersion makes catalog-bearing snapshots/logs unreadable to an
// older binary which only understands incident state. Existing version-1 stores
// remain readable, and do not require a destructive storage reset.
const CatalogFormatVersion = 2

var (
	ErrCatalogConflict   = errors.New("resource version or incarnation changed")
	ErrCatalogNotFound   = errors.New("resource not found")
	ErrCatalogReferenced = errors.New("resource is still referenced")
	ErrCatalogDependency = errors.New("resource dependency changed or is unavailable")
)

// CatalogKey contains only a resource kind and stable identity. Provider
// parameters, display metadata, secrets, and executable jobs belong in Payload
// or the runtime projection, never in this lookup key.
type CatalogKey struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

func (k CatalogKey) indexKey() string { return k.Kind + "\x00" + k.ID }

func (k CatalogKey) validate() error {
	if !catalogIdentifier(k.Kind, 64) || !catalogIdentifier(k.ID, 256) {
		return errors.New("invalid catalog resource identity")
	}
	return nil
}

func catalogIdentifier(s string, limit int) bool {
	return s != "" && len(s) <= limit && utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r\n")
}

// CatalogRecord is an immutable committed encrypted resource. References are
// non-secret identity edges; all provider configuration and editable metadata
// stay encrypted. DependentsVersion changes when a referencing resource changes,
// without changing this resource's own configuration revision.
type CatalogRecord struct {
	catalogRecordExtensions
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

func (r CatalogRecord) Clone() CatalogRecord {
	r.catalogRecordExtensions = cloneCatalogRecordExtensions(r)
	r.Payload = r.Payload.Clone()
	r.References = slices.Clone(r.References)
	return r
}

func (r CatalogRecord) Binding(storeID string) secureconfig.Binding {
	return secureconfig.Binding{StoreID: storeID, Kind: r.Key.Kind, ID: r.Key.ID,
		UID: r.UID, Revision: r.Revision, Purpose: r.Purpose}
}

func (r CatalogRecord) validate() error {
	if err := validateCatalogRecordExtensions(r); err != nil {
		return err
	}
	if err := r.Key.validate(); err != nil {
		return err
	}
	if !catalogIdentifier(r.UID, 256) || !catalogIdentifier(r.Revision, 256) || !catalogIdentifier(r.Purpose, 64) {
		return errors.New("invalid catalog record identity metadata")
	}
	if r.Generation == 0 {
		return errors.New("invalid catalog record generation")
	}
	if r.CreatedAt.IsZero() || r.UpdatedAt.Before(r.CreatedAt) {
		return errors.New("invalid catalog record timestamp ordering")
	}
	// A tombstone keeps identity/version for safe delete/recreate and history,
	// but no provider payload or outbound references.
	if r.Removed {
		if len(r.References) != 0 || r.Payload.Format != 0 || r.Payload.KeyID != "" ||
			len(r.Payload.WrappedKey) != 0 || len(r.Payload.Nonce) != 0 || len(r.Payload.Ciphertext) != 0 {
			return errors.New("catalog tombstone contains an active payload")
		}
		return nil
	}
	if err := r.Payload.Validate(); err != nil {
		return errors.New("invalid encrypted catalog payload")
	}
	seen := make(map[CatalogKey]struct{}, len(r.References))
	for _, key := range r.References {
		if err := key.validate(); err != nil {
			return err
		}
		if key == r.Key {
			return errors.New("catalog resource cannot reference itself")
		}
		if _, ok := seen[key]; ok {
			return errors.New("duplicate catalog reference")
		}
		seen[key] = struct{}{}
	}
	return nil
}

// CatalogCondition freezes a read which justified a resource mutation. Checking
// it inside Apply prevents a valid preflight from binding to a changed target.
type CatalogCondition struct {
	Key                       CatalogKey `json:"key"`
	UID                       string     `json:"uid"`
	Revision                  string     `json:"revision"`
	ExpectedDependentsVersion *uint64    `json:"expected_dependents_version,omitempty"`
}

// CatalogMutation changes exactly one resource. Submission batching does not
// make unrelated resources one atomic collection. New records and cryptographic
// randomness are prepared before Submit, never generated during replay.
type CatalogMutation struct {
	OperationID               string             `json:"operation_id,omitempty"`
	Actor                     string             `json:"actor,omitempty"`
	Record                    CatalogRecord      `json:"record"`
	Create                    bool               `json:"create,omitempty"`
	ExpectedUID               string             `json:"expected_uid,omitempty"`
	ExpectedRevision          string             `json:"expected_revision,omitempty"`
	ExpectedDependentsVersion uint64             `json:"expected_dependents_version"`
	Conditions                []CatalogCondition `json:"conditions,omitempty"`
}

func (m CatalogMutation) Clone() CatalogMutation {
	m.Record = m.Record.Clone()
	m.Conditions = slices.Clone(m.Conditions)
	for i := range m.Conditions {
		if expected := m.Conditions[i].ExpectedDependentsVersion; expected != nil {
			version := *expected
			m.Conditions[i].ExpectedDependentsVersion = &version
		}
	}
	return m
}

func (m CatalogMutation) validate() error {
	if m.Record.Key.Kind == "JobType" {
		return errors.New("JobType requires its dedicated storage contract")
	}
	if m.OperationID != "" {
		if !validOperationIdentity(m.OperationID, m.Record.Revision) || !catalogIdentifier(m.Actor, 128) {
			return errors.New("invalid catalog operation identity")
		}
	} else if m.Actor != "" {
		return errors.New("catalog actor requires an operation identity")
	}
	if err := m.Record.validate(); err != nil {
		return err
	}
	if m.Record.CommittedIndex != 0 || m.Record.DependentsVersion != 0 {
		return errors.New("catalog submission contains server-owned commit state")
	}
	if m.Create {
		if m.Record.Removed || m.ExpectedUID != "" || m.ExpectedRevision != "" ||
			m.Record.Generation != 1 || m.ExpectedDependentsVersion != 0 {
			return errors.New("invalid catalog create precondition")
		}
	} else if !catalogIdentifier(m.ExpectedUID, 256) || !catalogIdentifier(m.ExpectedRevision, 256) {
		return errors.New("catalog changes require a version and incarnation precondition")
	}
	seen := make(map[CatalogKey]struct{}, len(m.Conditions))
	for _, condition := range m.Conditions {
		if err := condition.Key.validate(); err != nil {
			return err
		}
		if !catalogIdentifier(condition.UID, 256) || !catalogIdentifier(condition.Revision, 256) {
			return errors.New("catalog dependency requires version and incarnation")
		}
		if _, ok := seen[condition.Key]; ok {
			return errors.New("duplicate catalog condition")
		}
		seen[condition.Key] = struct{}{}
	}
	for _, key := range m.Record.References {
		if _, ok := seen[key]; !ok {
			return errors.New("catalog reference has no frozen dependency condition")
		}
	}
	return nil
}

type catalogItem struct {
	key    string
	record CatalogRecord
}

func newCatalogTree() *btree.BTreeG[catalogItem] {
	return btree.NewG[catalogItem](32, func(a, b catalogItem) bool { return a.key < b.key })
}

// rebuildCatalogIndexes is run before admission following restoration. The
// payloads remain immutable; the indexes contain no decrypted provider state.
func (f *machine) rebuildCatalogIndexes() {
	_ = f.rebuildCatalogIndexesContext(context.Background())
}

func (f *machine) rebuildCatalogIndexesContext(ctx context.Context) error {
	catalog := newCatalogTree()
	incomingByKey := make(map[string]map[string]struct{})
	for key, record := range f.image.Catalog {
		if err := ctx.Err(); err != nil {
			return err
		}
		if record.Removed {
			continue
		}
		catalog.ReplaceOrInsert(catalogItem{key: key, record: record})
		addCatalogRecordExtensions(incomingByKey, record)
		for _, target := range record.References {
			if err := ctx.Err(); err != nil {
				return err
			}
			incoming := incomingByKey[target.indexKey()]
			if incoming == nil {
				incoming = make(map[string]struct{})
				incomingByKey[target.indexKey()] = incoming
			}
			incoming[key] = struct{}{}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	f.catalog, f.catalogIncoming = catalog, incomingByKey
	return nil
}

func (f *machine) applyCatalog(m CatalogMutation, index uint64, at time.Time, format int) Result {
	prepared, err := f.prepareCatalogMutation(m, index, at, format)
	if err != nil {
		return Result{Err: err}
	}
	if err := f.persistCollectionChildTerminals(collectionCatalogTerminals(prepared)); err != nil {
		return f.collectionStorageFailure(err)
	}
	return f.installCatalogMutation(prepared)
}

func validateCatalogImage(i image) error {
	if i.Version < CatalogMutationFormatVersion && i.CatalogMutationSequence != 0 {
		return errors.New("catalog mutation sequence requires format 4")
	}
	if i.Version == CatalogMutationFormatVersion && i.CatalogMutationSequence == 0 {
		return errors.New("format 4 requires its catalog mutation sequence")
	}
	if i.Version == FormatVersion && len(i.Catalog) != 0 {
		return errors.New("catalog state requires the catalog snapshot format")
	}
	for key, record := range i.Catalog {
		if err := validateCatalogRecordImageExtensions(i, record, false); err != nil {
			return err
		}
		if record.Key.Kind == "JobType" {
			return errors.New("JobType cannot inhabit the ordinary catalog")
		}
		if key != record.Key.indexKey() {
			return errors.New("catalog identity does not match its index")
		}
		if err := record.validate(); err != nil {
			return err
		}
		maximum := i.Index
		if i.CatalogMutationSequence != 0 {
			maximum = i.CatalogMutationSequence
		}
		if record.CommittedIndex == 0 || record.CommittedIndex > i.Index || record.DependentsVersion > maximum {
			return errors.New("invalid catalog commit position")
		}
		for _, target := range record.References {
			current, ok := i.Catalog[target.indexKey()]
			if !ok || current.Removed {
				return errors.New("catalog snapshot has a missing reference")
			}
		}
	}
	return nil
}

// CatalogGet returns a private copy of one active committed resource.
func (s *Store) CatalogGet(key CatalogKey) (CatalogRecord, bool, error) {
	if err := key.validate(); err != nil {
		return CatalogRecord{}, false, err
	}
	s.fsm.mu.RLock()
	defer s.fsm.mu.RUnlock()
	if s.fsm.bootstrapPending() {
		return CatalogRecord{}, false, ErrBootstrapPending
	}
	if s.fsm.err != nil {
		return CatalogRecord{}, false, errors.New("durable catalog unavailable")
	}
	record, ok := s.fsm.image.Catalog[key.indexKey()]
	if !ok || record.Removed {
		return CatalogRecord{}, false, nil
	}
	return record.Clone(), true, nil
}

// CatalogView is a consistent immutable read generation. API cursor ownership,
// expiry, filters, authorization and retention limits are enforced by its caller.
type CatalogView struct {
	tree   *btree.BTreeG[catalogItem]
	Index  uint64
	Cursor CatalogCursor
}

// Len returns the active resource count in this generation. A caller validating
// known kinds can use it to detect unsupported kinds without materializing the
// complete catalog or silently ignoring unreadable resources.
func (v CatalogView) Len() int {
	if v.tree == nil {
		return 0
	}
	return v.tree.Len()
}

// CatalogSnapshot takes the tree's O(1) copy-on-write clone under the writer
// lock. Pages are constructed outside the Raft/controller execution path.
func (s *Store) CatalogSnapshot() (CatalogView, error) {
	return s.CatalogSnapshotContext(context.Background())
}

// CatalogSnapshotContext captures the encrypted catalog index within ctx.
func (s *Store) CatalogSnapshotContext(ctx context.Context) (CatalogView, error) {
	unlock, err := s.lockCatalogReadState(ctx, true)
	if err != nil {
		return CatalogView{}, err
	}
	defer unlock()
	if s.fsm.catalog == nil {
		if err := s.fsm.rebuildCatalogIndexesContext(ctx); err != nil {
			return CatalogView{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return CatalogView{}, err
	}
	return CatalogView{tree: s.fsm.catalog.Clone(), Index: s.fsm.image.Index, Cursor: CatalogCursor{owner: s.fsm, epoch: s.fsm.catalogEpoch, position: s.fsm.catalogSequence}}, nil
}

// Page returns no more than limit resources and an exclusive stable-ID boundary.
// The caller must retain this same view when continuing its snapshot.
func (v CatalogView) Page(kind, after string, limit int) ([]CatalogRecord, string, error) {
	if v.tree == nil || !catalogIdentifier(kind, 64) ||
		(after != "" && !catalogIdentifier(after, 256)) || limit < 1 || limit > 500 {
		return nil, "", errors.New("invalid catalog page request")
	}
	prefix := kind + "\x00"
	start := prefix + after
	records := make([]CatalogRecord, 0, limit)
	next := ""
	v.tree.AscendGreaterOrEqual(catalogItem{key: start}, func(item catalogItem) bool {
		if !strings.HasPrefix(item.key, prefix) {
			return false
		}
		if after != "" && item.key == start {
			return true
		}
		if len(records) == limit {
			next = records[len(records)-1].Key.ID
			return false
		}
		records = append(records, item.record.Clone())
		return true
	})
	return records, next, nil
}

func (v CatalogView) Get(key CatalogKey) (CatalogRecord, bool) {
	if v.tree == nil {
		return CatalogRecord{}, false
	}
	item, ok := v.tree.Get(catalogItem{key: key.indexKey()})
	if !ok {
		return CatalogRecord{}, false
	}
	return item.record.Clone(), true
}

// CatalogDependents returns a bounded set plus the guard revision used by a
// shared-resource preflight. A larger set must use an asynchronous validation
// path, never silently validate only the first page.
func (s *Store) CatalogDependents(key CatalogKey, limit int) ([]CatalogKey, uint64, error) {
	return s.CatalogDependentsContext(context.Background(), key, limit)
}

// CatalogDependentsContext reads a bounded reverse-dependency set within ctx.
func (s *Store) CatalogDependentsContext(ctx context.Context, key CatalogKey, limit int) ([]CatalogKey, uint64, error) {
	if err := key.validate(); err != nil {
		return nil, 0, err
	}
	if limit < 1 || limit > 10000 {
		return nil, 0, errors.New("invalid dependent validation limit")
	}
	unlock, err := s.lockCatalogReadState(ctx, false)
	if err != nil {
		return nil, 0, err
	}
	defer unlock()
	record, ok := s.fsm.image.Catalog[key.indexKey()]
	if !ok || record.Removed {
		return nil, 0, ErrCatalogNotFound
	}
	incoming := s.fsm.catalogIncoming[key.indexKey()]
	if len(incoming) > limit {
		return nil, 0, fmt.Errorf("resource has more than %d dependents; use collection validation", limit)
	}
	keys := make([]CatalogKey, 0, len(incoming))
	for dependent := range incoming {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		keys = append(keys, s.fsm.image.Catalog[dependent].Key)
	}
	slices.SortFunc(keys, func(a, b CatalogKey) int { return strings.Compare(a.indexKey(), b.indexKey()) })
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	return keys, record.DependentsVersion, nil
}
