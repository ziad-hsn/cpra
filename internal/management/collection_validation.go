package management

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"

	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

const collectionValidationBytes = 32 << 20

// Repeated prefix checks have a separate CPU-work bound, even when all resource
// versions fit in memory. A larger operation requires a future staged validator.
const collectionValidationVisits = 100000

// CollectionInput carries an already decoded desired resource. SourceID and
// ItemID are opaque caller-assigned tokens, not filenames, URLs, or diagnostics.
// The caller must authorize all submitted writes before calling ValidateCollection.
type CollectionInput struct {
	Resource api.Resource
	SourceID string
	ItemID   string
}

type CollectionValidationOptions struct {
	// CanRead authorizes identities only. It is required before querying any target identity,
	// reference and outside dependent; it never receives resource plaintext.
	CanRead func(persistence.CatalogKey) bool
}

type CollectionValidationItem struct {
	SourceID        string
	ItemID          string
	Key             persistence.CatalogKey
	Change          string // create, update, unchanged; empty when input is invalid
	Issue           string // safe classification, never a provider/parser error
	UID             string
	ResourceVersion string
	Dependencies    []persistence.CatalogKey
}

// CollectionValidation contains observations, not executable prepared mutations.
// Conditions and CreateAbsent refer to the original captured generation. A future
// executor must revalidate/rebase only its own confirmed prior writes, preserve
// outside reverse-edge guards, and never fall back to an old included dependency.
// This helper neither authorizes writes nor guarantees a later CAS will succeed.
type CollectionValidation struct {
	Valid        bool
	Index        uint64
	Items        []CollectionValidationItem
	Order        []persistence.CatalogKey
	Conditions   []persistence.CatalogCondition
	CreateAbsent []persistence.CatalogKey
	Impacted     []persistence.CatalogKey
}

type collectionValidator struct {
	catalog                 *Catalog
	view                    persistence.CatalogView
	options                 CollectionValidationOptions
	result                  CollectionValidation
	desired                 map[persistence.CatalogKey]api.Resource
	live                    map[persistence.CatalogKey]api.Resource
	records                 map[persistence.CatalogKey]persistence.CatalogRecord
	item                    map[persistence.CatalogKey]int
	cause                   map[persistence.CatalogKey]int
	reverseGuards           map[persistence.CatalogKey]uint64
	bytes, versions, visits int
}

// ValidateCollection performs bounded request-local staged-union and ordered
// prefix validation. It reads encrypted live records but never seals, allocates
// operations/keys, writes staging, or constructs/invokes executable jobs.
func (c *Catalog) ValidateCollection(ctx context.Context, captured ReadView, inputs []CollectionInput, options CollectionValidationOptions) (CollectionValidation, error) {
	result := CollectionValidation{Index: captured.Index(), Items: []CollectionValidationItem{}}
	if ctx == nil || options.CanRead == nil || captured.catalog != c {
		return result, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !c.Ready() {
		return result, ErrUnavailable
	}
	if len(inputs) > maxValidationGraph {
		return result, ErrGraphLimit
	}
	v := collectionValidator{catalog: c, view: captured.view, options: options, result: result,
		desired: map[persistence.CatalogKey]api.Resource{}, live: map[persistence.CatalogKey]api.Resource{}, records: map[persistence.CatalogKey]persistence.CatalogRecord{}, item: map[persistence.CatalogKey]int{}, cause: map[persistence.CatalogKey]int{}, reverseGuards: map[persistence.CatalogKey]uint64{}}
	defer func() {
		for _, r := range v.desired {
			clear(r.Spec)
		}
		for _, r := range v.live {
			clear(r.Spec)
		}
	}()
	for i, input := range inputs {
		if err := ctx.Err(); err != nil {
			return v.result, err
		}
		item := CollectionValidationItem{}
		if collectionSourceToken(input.SourceID) && collectionSourceToken(input.ItemID) {
			item.SourceID, item.ItemID = input.SourceID, input.ItemID
		} else {
			item.Issue = "invalidSource"
		}
		v.result.Items = append(v.result.Items, item)
		if item.Issue != "" {
			continue
		}
		if len(input.Resource.Spec) > api.MaxResourceBytes || len(input.Resource.Status) > api.MaxResourceBytes || validateMetadata(input.Resource.Metadata) != nil || len(input.Resource.Metadata.ID) > 256 || len(input.Resource.Metadata.UID) > 256 || len(input.Resource.Metadata.ResourceVersion) > 256 || len(input.Resource.Kind) > 64 || len(input.Resource.APIVersion) > 64 {
			v.result.Items[i].Issue = "invalidResource"
			continue
		}
		raw, err := json.Marshal(input.Resource)
		if err != nil || len(raw) > api.MaxResourceBytes {
			clear(raw)
			v.result.Items[i].Issue = "invalidResource"
			continue
		}
		if err := v.charge(len(raw) + len(input.SourceID) + len(input.ItemID) + 512); err != nil {
			clear(raw)
			return v.reject(i, err)
		}
		originalBytes := len(raw)
		r, err := api.DecodeResource(raw)
		clear(raw)
		if err != nil || !supportedKind(r.Kind) || !validID(r.Metadata.ID) || validateMetadata(r.Metadata) != nil {
			v.result.Items[i].Issue = "invalidResource"
			continue
		}
		if r.Kind == "Monitor" {
			if _, err := (manifest.Monitor{ID: r.Metadata.ID}).EffectiveID(); err != nil {
				v.result.Items[i].Issue = "invalidResource"
				continue
			}
		}
		key := persistence.CatalogKey{Kind: r.Kind, ID: r.Metadata.ID}
		v.result.Items[i].Key = key
		if previous, ok := v.item[key]; ok {
			v.result.Items[i].Issue = "duplicateIdentity"
			v.result.Items[previous].Issue = "duplicateIdentity"
			continue
		}
		v.item[key], v.cause[key] = i, i
		old, exists, err := v.load(ctx, key, i)
		if err != nil {
			return v.reject(i, err)
		}
		if exists {
			if r.Metadata.UID != "" && r.Metadata.UID != old.Metadata.UID || r.Metadata.ResourceVersion != "" && r.Metadata.ResourceVersion != old.Metadata.ResourceVersion || r.Metadata.Generation != 0 && r.Metadata.Generation != old.Metadata.Generation {
				return v.reject(i, persistence.ErrCatalogConflict)
			}
			if r.Kind == "Credential" {
				if err := preserveCredential(&r, old); err != nil {
					v.result.Items[i].Issue = "invalidResource"
					continue
				}
			}
			r.Metadata.UID, r.Metadata.ResourceVersion, r.Metadata.Generation = old.Metadata.UID, old.Metadata.ResourceVersion, old.Metadata.Generation
			v.result.Items[i].UID, v.result.Items[i].ResourceVersion = old.Metadata.UID, old.Metadata.ResourceVersion
			v.result.Items[i].Change = "update"
		} else {
			if r.Metadata.UID != "" || r.Metadata.ResourceVersion != "" || r.Metadata.Generation != 0 {
				return v.reject(i, persistence.ErrCatalogConflict)
			}
			v.result.Items[i].Change = "create"
		}
		r.Status = nil
		if validateDesired(&r) != nil {
			v.result.Items[i].Issue = "invalidResource"
			continue
		}
		var original *api.Resource
		if exists {
			original = &old
		}
		shape, shapeErr := collectionCandidateEncoding(r, original)
		if shapeErr != nil {
			v.result.Items[i].Issue = "invalidResource"
			continue
		}
		normalized, err := json.Marshal(r)
		refs, refErr := directReferences(r)
		if err != nil || len(normalized) > api.MaxResourceBytes || refErr != nil {
			clear(normalized)
			v.result.Items[i].Issue = "invalidResource"
			continue
		}
		extra := max(0, len(normalized)-originalBytes) + len(refs)*320
		clear(normalized)
		if extra > collectionValidationBytes-v.bytes {
			return v.reject(i, ErrGraphLimit)
		}
		v.bytes += extra
		slices.SortFunc(refs, collectionCompareKey)
		v.result.Items[i].Dependencies = refs
		if !shape.Changed {
			v.result.Items[i].Change = "unchanged"
		}
		v.desired[key] = r
	}
	for _, item := range v.result.Items {
		if item.Issue != "" {
			return v.result, ErrValidation
		}
	}
	if err := v.expand(ctx); err != nil {
		return v.result, err
	}
	final := maps.Clone(v.live)
	for key, r := range v.desired {
		final[key] = r
	}
	if key, err := v.validateSet(ctx, final); err != nil {
		return v.reject(v.cause[key], err)
	}
	order, err := v.order(ctx)
	if err != nil {
		return v.reject(0, err)
	}
	if err := v.prefixes(ctx, order); err != nil {
		return v.result, err
	}
	// Fresh immutable reads reject already-stale validation; the returned original
	// guards still remain necessary for every later activation.
	current, err := c.store.CatalogSnapshot()
	if err != nil {
		return v.result, ErrUnavailable
	}
	for _, key := range collectionKeys(v.records) {
		if err := ctx.Err(); err != nil {
			return v.result, err
		}
		record := v.records[key]
		now, ok := current.Get(key)
		guard, guarded := v.reverseGuards[key]
		if !ok || now.UID != record.UID || now.Revision != record.Revision || guarded && now.DependentsVersion != guard {
			return v.reject(v.cause[key], persistence.ErrCatalogConflict)
		}
		condition := persistence.CatalogCondition{Key: key, UID: record.UID, Revision: record.Revision}
		if guarded {
			condition.ExpectedDependentsVersion = api.Pointer(guard)
		}
		v.result.Conditions = append(v.result.Conditions, condition)
	}
	for _, key := range order {
		if _, exists := v.records[key]; !exists {
			if _, exists := current.Get(key); exists {
				return v.reject(v.item[key], persistence.ErrCatalogConflict)
			}
			v.result.CreateAbsent = append(v.result.CreateAbsent, key)
		}
	}
	if !c.Ready() {
		return v.result, ErrUnavailable
	}
	v.result.Valid, v.result.Order = true, order
	return v.result, nil
}

func collectionSourceToken(s string) bool {
	if len(s) < 1 || len(s) > 128 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return s != "." && s != ".."
}
func collectionDesiredEqual(a, b api.Resource) bool {
	a.Status, b.Status = nil, nil
	a.Metadata.UID, a.Metadata.ResourceVersion, b.Metadata.UID, b.Metadata.ResourceVersion = "", "", "", ""
	a.Metadata.Generation, b.Metadata.Generation = 0, 0
	ar, _ := json.Marshal(a)
	br, _ := json.Marshal(b)
	defer clear(ar)
	defer clear(br)
	return jsonEqual(ar, br)
}
func collectionKeys[T any](values map[persistence.CatalogKey]T) []persistence.CatalogKey {
	keys := slices.Collect(maps.Keys(values))
	slices.SortFunc(keys, func(a, b persistence.CatalogKey) int {
		if n := strings.Compare(a.Kind, b.Kind); n != 0 {
			return n
		}
		return strings.Compare(a.ID, b.ID)
	})
	return keys
}
func (v *collectionValidator) charge(n int) error {
	if n < 0 || n > collectionValidationBytes-v.bytes || v.versions >= maxValidationGraph {
		return ErrGraphLimit
	}
	v.bytes += n
	v.versions++
	return nil
}
func (v *collectionValidator) reject(i int, err error) (CollectionValidation, error) {
	code := "invalidGraph"
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return v.result, err
	case errors.Is(err, ErrGraphLimit):
		code = "validationLimit"
	case errors.Is(err, ErrUnavailable):
		code = "unavailable"
	case errors.Is(err, persistence.ErrCatalogConflict):
		code = "conflict"
	case errors.Is(err, errCollectionReadDenied):
		code = "readDenied"
	case errors.Is(err, persistence.ErrCatalogDependency):
		code = "missingReference"
	}
	if i >= 0 && i < len(v.result.Items) {
		v.result.Items[i].Issue = code
	}
	v.result.Valid = false
	v.result.Order = nil
	v.result.Conditions = nil
	v.result.CreateAbsent = nil
	return v.result, err
}

var errCollectionReadDenied = errors.New("collection validation requires access to every observed resource")

func (v *collectionValidator) load(ctx context.Context, key persistence.CatalogKey, cause int) (api.Resource, bool, error) {
	if r, ok := v.live[key]; ok {
		return r, true, nil
	}
	if err := ctx.Err(); err != nil {
		return api.Resource{}, false, err
	}
	// Authorize the identity before testing existence, including new staged
	// targets, so denied existing and absent names are indistinguishable.
	if !v.options.CanRead(key) {
		return api.Resource{}, false, errCollectionReadDenied
	}
	record, exists := v.view.Get(key)
	if !exists {
		return api.Resource{}, false, nil
	}
	if err := v.charge(len(record.Payload.Ciphertext) + 512 + len(record.Key.Kind) + len(record.Key.ID) + len(record.UID) + len(record.Revision) + len(record.References)*320); err != nil {
		return api.Resource{}, false, err
	}
	r, err := v.catalog.open(ctx, record)
	if err != nil {
		return api.Resource{}, false, err
	}
	// Keep only scalar guards after decrypting. In particular a backend may
	// return a large wrapped key; none of the envelope belongs in this cache.
	clear(record.Payload.Ciphertext)
	clear(record.Payload.WrappedKey)
	clear(record.Payload.Nonce)
	record = persistence.CatalogRecord{Key: record.Key, UID: record.UID, Revision: record.Revision, DependentsVersion: record.DependentsVersion}
	v.live[key], v.records[key], v.cause[key] = r, record, cause
	return r, true, nil
}

func (v *collectionValidator) expand(ctx context.Context) error {
	// Load both old and staged edges. Old consumers must remain valid at each
	// dependency-first prefix, even when a later included edit will replace them.
	loaded := map[persistence.CatalogKey]bool{}
	var dependencies func(persistence.CatalogKey, int) error
	dependencies = func(key persistence.CatalogKey, cause int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if loaded[key] {
			return nil
		}
		loaded[key] = true
		_, _, err := v.load(ctx, key, cause)
		if err != nil {
			return err
		}
		versions := []api.Resource{}
		if r, ok := v.live[key]; ok {
			versions = append(versions, r)
		}
		if r, ok := v.desired[key]; ok {
			versions = append(versions, r)
		}
		if len(versions) == 0 {
			return persistence.ErrCatalogDependency
		}
		for _, r := range versions {
			refs, err := directReferences(r)
			if err != nil {
				return ErrValidation
			}
			for _, ref := range refs {
				if err := dependencies(ref, cause); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, key := range collectionKeys(v.desired) {
		if err := dependencies(key, v.item[key]); err != nil {
			_, err = v.reject(v.item[key], err)
			return err
		}
	}
	queue := []persistence.CatalogKey{}
	for _, key := range collectionKeys(v.desired) {
		if _, exists := v.records[key]; exists && v.result.Items[v.item[key]].Change != "unchanged" {
			queue = append(queue, key)
		}
	}
	seen := map[persistence.CatalogKey]bool{}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		key := queue[0]
		queue = queue[1:]
		if seen[key] {
			continue
		}
		seen[key] = true
		record := v.records[key]
		dependents, guard, err := v.catalog.store.CatalogDependents(key, maxValidationGraph)
		if err != nil {
			if errors.Is(err, persistence.ErrCatalogNotFound) {
				err = persistence.ErrCatalogConflict
			} else if !v.catalog.Ready() {
				err = ErrUnavailable
			} else {
				err = ErrGraphLimit
			}
			_, err = v.reject(v.cause[key], err)
			return err
		}
		if guard != record.DependentsVersion {
			_, err = v.reject(v.cause[key], persistence.ErrCatalogConflict)
			return err
		}
		v.reverseGuards[key] = guard
		for _, dependent := range dependents {
			if err := dependencies(dependent, v.cause[key]); err != nil {
				_, err = v.reject(v.cause[key], err)
				return err
			}
			if _, exists := v.records[dependent]; !exists {
				_, err = v.reject(v.cause[key], persistence.ErrCatalogConflict)
				return err
			}
			if _, included := v.item[dependent]; !included && !seen[dependent] {
				v.result.Impacted = append(v.result.Impacted, dependent)
			}
			queue = append(queue, dependent)
		}
	}
	v.result.Impacted = slices.CompactFunc(slices.SortedFunc(slices.Values(v.result.Impacted), collectionCompareKey), func(a, b persistence.CatalogKey) bool { return a == b })
	return nil
}
func collectionCompareKey(a, b persistence.CatalogKey) int {
	if n := strings.Compare(a.Kind, b.Kind); n != 0 {
		return n
	}
	return strings.Compare(a.ID, b.ID)
}

func (v *collectionValidator) order(ctx context.Context) ([]persistence.CatalogKey, error) {
	seen := map[persistence.CatalogKey]int{}
	order := []persistence.CatalogKey{}
	var visit func(persistence.CatalogKey) error
	visit = func(key persistence.CatalogKey) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if seen[key] == 1 {
			return ErrValidation
		}
		if seen[key] == 2 {
			return nil
		}
		seen[key] = 1
		refs, err := directReferences(v.desired[key])
		if err != nil {
			return ErrValidation
		}
		slices.SortFunc(refs, collectionCompareKey)
		for _, ref := range refs {
			if _, included := v.desired[ref]; included {
				if err := visit(ref); err != nil {
					return err
				}
			}
		}
		seen[key] = 2
		order = append(order, key)
		return nil
	}
	for _, kind := range ResourceKinds() {
		for _, key := range collectionKeys(v.desired) {
			if key.Kind == kind {
				if err := visit(key); err != nil {
					return nil, err
				}
			}
		}
	}
	return order, nil
}
