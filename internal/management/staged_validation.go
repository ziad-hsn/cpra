package management

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// The staged path bounds retained graph metadata separately from borrowed
// plaintext. The first slice still bounds graph versions and total work; it is
// not an arbitrary-cardinality or resumable validation implementation.
const stagedValidationMetadataBytes = 32 << 20

type stagedValidationVersion struct {
	refs              []persistence.CatalogKey
	uid, revision     string
	notificationType  string
	generation        int64
	dependentsVersion uint64
}

type stagedValidator struct {
	catalog       *Catalog
	captured      ReadView
	source        *collectionValidationSource
	options       CollectionValidationOptions
	result        CollectionValidation
	desired, live map[persistence.CatalogKey]stagedValidationVersion
	item, cause   map[persistence.CatalogKey]int
	reverseGuards map[persistence.CatalogKey]uint64
	bytes         int
	versions      int
	visits        int
	plan          *collectionPlanCompiler
}

// validateStagedCollection observes one fully authenticated encrypted inventory
// and one captured live generation. Only graph identities, edges and guards
// survive resource callbacks. It neither seals nor prepares/activates changes.
// Caller authorization of submitted writes and operation ownership is separate.
func (c *Catalog) validateStagedCollection(ctx context.Context, captured ReadView, source *collectionValidationSource, options CollectionValidationOptions) (CollectionValidation, error) {
	return c.validateStagedCollectionWithPlan(ctx, captured, source, options, nil)
}

func (c *Catalog) validateStagedCollectionWithPlan(ctx context.Context, captured ReadView, source *collectionValidationSource, options CollectionValidationOptions, plan *collectionPlanCompiler) (CollectionValidation, error) {
	result := CollectionValidation{Index: captured.Index(), Items: []CollectionValidationItem{}}
	if ctx == nil || source == nil || source.catalog != c || captured.catalog != c || options.CanRead == nil {
		return result, ErrValidation
	}
	if err := source.check(ctx); err != nil {
		return result, err
	}
	if source.head.ItemCount > maxValidationGraph {
		return result, ErrGraphLimit
	}
	v := stagedValidator{catalog: c, captured: captured, source: source, options: options, result: result, plan: plan,
		desired: map[persistence.CatalogKey]stagedValidationVersion{}, live: map[persistence.CatalogKey]stagedValidationVersion{},
		item: map[persistence.CatalogKey]int{}, cause: map[persistence.CatalogKey]int{}, reverseGuards: map[persistence.CatalogKey]uint64{}}
	failedItem := -1
	if err := source.walk(ctx, func(ref stagedItemRef, input *api.Resource) error {
		err := v.ingest(ctx, ref, input)
		if err != nil {
			failedItem = len(v.result.Items) - 1
		}
		return err
	}); err != nil {
		var positioned *collectionSourceItemError
		if errors.As(err, &positioned) && positioned.ref.Ordinal > 0 && positioned.ref.Ordinal <= source.head.ItemCount {
			ordinal := positioned.ref.Ordinal
			if ordinal == uint64(len(v.result.Items))+1 {
				v.result.Items = append(v.result.Items, CollectionValidationItem{SourceID: positioned.ref.Source,
					ItemID: fmt.Sprintf("item.%020d", ordinal), Key: positioned.ref.Key})
			}
			if ordinal <= uint64(len(v.result.Items)) {
				failedItem = int(ordinal - 1)
			}
		}
		// Global inventory/page errors have no item. They must never blame the
		// preceding valid row merely because it was the last observed callback.
		return v.reject(failedItem, err)
	}
	if uint64(len(v.result.Items)) != source.head.ItemCount {
		return v.reject(0, ErrUnavailable)
	}
	for _, item := range v.result.Items {
		if item.Issue != "" {
			return v.reject(-1, ErrValidation)
		}
	}
	if err := v.expand(ctx); err != nil {
		return v.result, err
	}
	final := make(map[persistence.CatalogKey]bool, len(v.live)+len(v.desired))
	for key := range v.live {
		final[key] = false
	}
	for key := range v.desired {
		final[key] = true
	}
	if key, err := v.validateSet(ctx, final); err != nil {
		return v.reject(v.cause[key], err)
	}
	order, err := v.order(ctx)
	if err != nil {
		return v.reject(0, err)
	}
	if plan != nil {
		if err := plan.order(ctx, order); err != nil {
			return v.reject(-1, err)
		}
	}
	if err := v.prefixes(ctx, order); err != nil {
		return v.result, err
	}
	if err := v.finish(ctx, order); err != nil {
		return v.result, err
	}
	v.result.Valid, v.result.Order = true, order
	return v.result, nil
}

func (v *stagedValidator) ingest(ctx context.Context, ref stagedItemRef, input *api.Resource) error {
	if v.plan != nil {
		if err := v.plan.source(ctx, ref); err != nil {
			return err
		}
	}
	i := len(v.result.Items)
	item := CollectionValidationItem{SourceID: ref.Source, ItemID: fmt.Sprintf("item.%020d", ref.Ordinal), Key: ref.Key}
	v.result.Items = append(v.result.Items, item)
	if !collectionSourceToken(item.SourceID) || !collectionSourceToken(item.ItemID) || ref.Ordinal != uint64(i+1) {
		v.result.Items[i].Issue = "invalidSource"
		return nil
	}
	if input == nil || ref.Key.Kind != input.Kind || ref.Key.ID != input.Metadata.ID ||
		!supportedKind(input.Kind) || !validID(input.Metadata.ID) || validateMetadata(input.Metadata) != nil ||
		len(input.Metadata.UID) > 256 || len(input.Metadata.ResourceVersion) > 256 || len(input.APIVersion) > 64 {
		v.result.Items[i].Issue = "invalidResource"
		return nil
	}
	if input.Kind == "Monitor" {
		if _, err := (manifest.Monitor{ID: input.Metadata.ID}).EffectiveID(); err != nil {
			v.result.Items[i].Issue = "invalidResource"
			return nil
		}
	}
	if previous, exists := v.item[ref.Key]; exists {
		v.result.Items[previous].Issue, v.result.Items[i].Issue = "duplicateIdentity", "duplicateIdentity"
		return nil
	}
	v.item[ref.Key], v.cause[ref.Key] = i, i
	err := v.normalized(ctx, ref.Key, input, func(resource *api.Resource, exists bool) {
		v.result.Items[i].Change = "create"
		if exists {
			v.result.Items[i].UID, v.result.Items[i].ResourceVersion = resource.Metadata.UID, resource.Metadata.ResourceVersion
			v.result.Items[i].Change = "update"
		}
	}, func(resource *api.Resource, old *api.Resource) error {
		refs, err := directReferences(*resource)
		if err != nil {
			return ErrValidation
		}
		version := stagedValidationVersion{refs: refs, uid: resource.Metadata.UID, revision: resource.Metadata.ResourceVersion, generation: resource.Metadata.Generation}
		version.notificationType, err = stagedNotificationType(resource)
		if err != nil {
			return err
		}
		if err := v.retain(ref.Key, version, len(item.SourceID)+len(item.ItemID)); err != nil {
			return err
		}
		slices.SortFunc(refs, collectionCompareKey)
		v.desired[ref.Key] = version
		v.result.Items[i].Dependencies = refs
		if old != nil {
			if collectionDesiredEqual(*resource, *old) {
				v.result.Items[i].Change = "unchanged"
			}
		}
		return nil
	})
	if errors.Is(err, ErrValidation) {
		v.result.Items[i].Issue = "invalidResource"
		return nil
	}
	return err
}

// retain charges scalar identities and conservative edge/map overhead before
// retaining slices or growing graph maps. It never charges aggregate plaintext.
func (v *stagedValidator) retain(key persistence.CatalogKey, version stagedValidationVersion, extra int) error {
	size := 1024 + extra + len(key.Kind) + len(key.ID) + len(version.uid) + len(version.revision) + len(version.notificationType)
	for _, ref := range version.refs {
		size += 320 + len(ref.Kind) + len(ref.ID)
		if size > stagedValidationMetadataBytes-v.bytes {
			return ErrGraphLimit
		}
	}
	if size > stagedValidationMetadataBytes-v.bytes || v.versions >= maxValidationGraph {
		return ErrGraphLimit
	}
	v.bytes += size
	v.versions++
	return nil
}

func (v *stagedValidator) visit(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	v.visits++
	if v.visits > collectionValidationVisits {
		return ErrGraphLimit
	}
	return nil
}

func (v *stagedValidator) reject(i int, err error) (CollectionValidation, error) {
	// Reuse the established public-safe item classifications without invoking
	// the request-local validator or allocating its plaintext resource maps.
	classified := collectionValidator{result: v.result}
	result, safe := classified.reject(i, stagedValidationError(err))
	v.result = result
	return result, safe
}

func stagedValidationError(err error) error {
	for _, allowed := range []error{context.Canceled, context.DeadlineExceeded, ErrGraphLimit, ErrUnavailable,
		persistence.ErrCatalogConflict, errCollectionReadDenied, persistence.ErrCatalogDependency, persistence.ErrOperationExpired,
		persistence.ErrCollectionConflict, persistence.ErrCollectionUnavailable} {
		if errors.Is(err, allowed) {
			if allowed == persistence.ErrCollectionUnavailable {
				return ErrUnavailable
			}
			return allowed
		}
	}
	return ErrValidation
}

func (v *stagedValidator) finish(ctx context.Context, order []persistence.CatalogKey) error {
	current, err := v.catalog.store.CatalogSnapshot()
	if err != nil {
		_, err = v.reject(0, ErrUnavailable)
		return err
	}
	for _, key := range collectionKeys(v.live) {
		if err := v.visit(ctx); err != nil {
			_, err = v.reject(v.cause[key], err)
			return err
		}
		old := v.live[key]
		now, exists := current.Get(key)
		guard, guarded := v.reverseGuards[key]
		matches := exists && now.UID == old.uid && now.Revision == old.revision && (!guarded || now.DependentsVersion == guard)
		clearStagedRecord(&now)
		if !matches {
			_, err = v.reject(v.cause[key], persistence.ErrCatalogConflict)
			return err
		}
		condition := persistence.CatalogCondition{Key: key, UID: old.uid, Revision: old.revision}
		if guarded {
			condition.ExpectedDependentsVersion = api.Pointer(guard)
		}
		v.result.Conditions = append(v.result.Conditions, condition)
	}
	for _, key := range order {
		if _, exists := v.live[key]; exists {
			continue
		}
		record, exists := current.Get(key)
		clearStagedRecord(&record)
		if exists {
			_, err = v.reject(v.item[key], persistence.ErrCatalogConflict)
			return err
		}
		v.result.CreateAbsent = append(v.result.CreateAbsent, key)
	}
	if err := v.source.check(ctx); err != nil {
		_, err = v.reject(-1, err)
		return err
	}
	return nil
}

func clearStagedRecord(record *persistence.CatalogRecord) {
	clear(record.Payload.Ciphertext)
	clear(record.Payload.WrappedKey)
	clear(record.Payload.Nonce)
}

// withLive copies one encrypted captured record, authorizes before existence,
// then borrows its opened resource only for the callback. Neither encrypted
// envelopes nor decrypted specs are cached in the graph.
func (v *stagedValidator) withLive(ctx context.Context, key persistence.CatalogKey, use func(*api.Resource) error) (bool, error) {
	if err := v.visit(ctx); err != nil {
		return false, err
	}
	if err := v.source.check(ctx); err != nil {
		return false, err
	}
	if !v.options.CanRead(key) || !v.source.canRead(key) {
		return false, errCollectionReadDenied
	}
	record, exists := v.captured.view.Get(key)
	if !exists {
		return false, v.source.check(ctx)
	}
	defer clearStagedRecord(&record)
	release, err := v.source.reserve(16*len(record.Payload.Ciphertext) + 4096 + len(record.References)*128)
	if err != nil {
		return false, err
	}
	defer release()
	resource, err := v.catalog.open(ctx, record)
	if err != nil {
		return false, stagedValidationError(err)
	}
	defer clear(resource.Spec)
	defer clear(resource.Status)
	if err := v.source.check(ctx); err != nil {
		return false, err
	}
	if _, known := v.live[key]; !known {
		version := stagedValidationVersion{refs: record.References, uid: record.UID, revision: record.Revision,
			generation: int64(record.Generation), dependentsVersion: record.DependentsVersion}
		version.notificationType, err = stagedNotificationType(&resource)
		if err != nil {
			return false, err
		}
		if err := v.retain(key, version, 0); err != nil {
			return false, err
		}
		version.refs = slices.Clone(version.refs)
		slices.SortFunc(version.refs, collectionCompareKey)
		v.live[key] = version
	}
	if err := use(&resource); err != nil {
		return true, err
	}
	return true, v.source.check(ctx)
}

// normalized owns one transient desired copy. Omitted credential values are
// borrowed from the captured old version, never written into the source ledger.
func (v *stagedValidator) normalized(ctx context.Context, key persistence.CatalogKey, input *api.Resource, observe func(*api.Resource, bool), use func(*api.Resource, *api.Resource) error) error {
	apply := func(old *api.Resource) error {
		size := len(input.Spec) + len(input.Status)
		if old != nil {
			size += len(old.Spec) + len(old.Status)
		}
		release, err := v.source.reserve(4096 + 16*size)
		if err != nil {
			return err
		}
		defer release()
		r := *input
		r.Spec, r.Status = slices.Clone(input.Spec), nil
		defer func() { clear(r.Spec) }()
		if old != nil {
			if r.Metadata.UID != "" && r.Metadata.UID != old.Metadata.UID || r.Metadata.ResourceVersion != "" && r.Metadata.ResourceVersion != old.Metadata.ResourceVersion || r.Metadata.Generation != 0 && r.Metadata.Generation != old.Metadata.Generation {
				return persistence.ErrCatalogConflict
			}
			if r.Kind == "Credential" {
				original := r.Spec
				err := preserveCredential(&r, *old)
				clear(original)
				if err != nil {
					return ErrValidation
				}
			}
			r.Metadata.UID, r.Metadata.ResourceVersion, r.Metadata.Generation = old.Metadata.UID, old.Metadata.ResourceVersion, old.Metadata.Generation
		} else if r.Metadata.UID != "" || r.Metadata.ResourceVersion != "" || r.Metadata.Generation != 0 {
			return persistence.ErrCatalogConflict
		}
		if observe != nil {
			observe(&r, old != nil)
		}
		original := r.Spec
		err = validateDesired(&r)
		// Credential validation does not replace Spec; clearing that still-owned
		// slice would erase the value before comparison or driver resolution.
		if r.Kind == "Monitor" || r.Kind == "NotificationEndpoint" {
			clear(original)
		}
		if err != nil {
			return ErrValidation
		}
		if _, err := collectionCandidateEncoding(r, old); err != nil {
			return ErrValidation
		}
		return use(&r, old)
	}
	found, err := v.withLive(ctx, key, func(old *api.Resource) error { return apply(old) })
	if err != nil {
		return err
	}
	if !found {
		return apply(nil)
	}
	return nil
}

type stagedSelectedLookup struct {
	validator *stagedValidator
	selected  map[persistence.CatalogKey]bool // present=false selects old; true selects desired
}

// Only the selected driver name survives an authenticated resource borrow. A
// routing check never needs its potentially large configuration. The original
// resource and resolved driver are still checked by validateSet for every
// selected version; this metadata is not a cached driver-validation result.
func stagedNotificationType(resource *api.Resource) (string, error) {
	if resource.Kind != "NotificationEndpoint" {
		return "", nil
	}
	var driver api.DriverConfig
	if err := api.StrictDecode(resource.Spec, &driver); err != nil {
		clear(driver.Config)
		return "", ErrValidation
	}
	defer clear(driver.Config)
	return strings.Clone(driver.Type), nil
}

// selectedVersion borrows bounded graph metadata retained after authenticated
// source/live decoding. Selected versions and the captured live view are
// immutable for this validation. Recheck authorization and source lifetime for
// every lookup; finish still fences live revisions before returning a plan.
func (l stagedSelectedLookup) selectedVersion(ctx context.Context, key persistence.CatalogKey) (stagedValidationVersion, bool, error) {
	v := l.validator
	if err := v.visit(ctx); err != nil {
		return stagedValidationVersion{}, false, err
	}
	if err := v.source.check(ctx); err != nil {
		return stagedValidationVersion{}, false, err
	}
	if !v.options.CanRead(key) || !v.source.canRead(key) {
		return stagedValidationVersion{}, false, errCollectionReadDenied
	}
	if err := v.source.check(ctx); err != nil {
		return stagedValidationVersion{}, false, err
	}
	desired, selected := l.selected[key]
	if !selected {
		return stagedValidationVersion{}, false, nil
	}
	versions := v.live
	if desired {
		versions = v.desired
	}
	version, exists := versions[key]
	if !exists {
		return stagedValidationVersion{}, false, ErrValidation
	}
	return version, true, nil
}

func (l stagedSelectedLookup) routingExists(ctx context.Context, key persistence.CatalogKey) (bool, error) {
	_, exists, err := l.selectedVersion(ctx, key)
	if err != nil {
		return false, err
	}
	return exists, l.validator.source.check(ctx)
}

func (l stagedSelectedLookup) withRoutingEndpoint(ctx context.Context, key persistence.CatalogKey, fn func(api.Metadata, string) error) (bool, error) {
	version, exists, err := l.selectedVersion(ctx, key)
	if err != nil || !exists {
		return false, err
	}
	if key.Kind != "NotificationEndpoint" || version.notificationType == "" || fn == nil {
		return false, ErrValidation
	}
	meta := api.Metadata{ID: key.ID, UID: version.uid, ResourceVersion: version.revision, Generation: version.generation}
	if err := fn(meta, version.notificationType); err != nil {
		return true, err
	}
	return true, l.validator.source.check(ctx)
}

func (l stagedSelectedLookup) withResource(ctx context.Context, key persistence.CatalogKey, use func(*api.Resource) error) (bool, error) {
	v := l.validator
	if err := v.visit(ctx); err != nil {
		return false, err
	}
	if !v.options.CanRead(key) || !v.source.canRead(key) {
		return false, errCollectionReadDenied
	}
	desired, selected := l.selected[key]
	if !selected {
		return false, nil
	}
	if !desired {
		return v.withLive(ctx, key, use)
	}
	return v.source.withResource(ctx, key, func(input *api.Resource) error {
		var prepared api.Resource
		var release func()
		defer func() {
			clear(prepared.Spec)
			if release != nil {
				release()
			}
		}()
		if err := v.normalized(ctx, key, input, nil, func(r *api.Resource, _ *api.Resource) error {
			var err error
			release, err = v.source.reserve(len(r.Spec) + 1024)
			if err != nil {
				return err
			}
			prepared = *r
			prepared.Spec = slices.Clone(r.Spec)
			return nil
		}); err != nil {
			return err
		}
		// Release the old live resource and normalization scratch before nested
		// driver/routing lookups. Only this reserved output and the original
		// source borrow remain live across the consumer callback.
		return use(&prepared)
	})
}
