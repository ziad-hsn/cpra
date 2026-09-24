package management

import (
	"context"
	"slices"

	"github.com/ziad-hsn/cpra/internal/persistence"
)

// A collectionPlan is private validation evidence, not a durable plan or an
// execution capability. It has no authorization grant, prepared mutation,
// plaintext resource, encryption key, or executable job. A future durable
// protocol must seal its complete inventory and reauthorize activation/steps.
// Runtime validation may strengthen these guards, never refresh or weaken them.
type collectionPlan struct {
	Header collectionPlanHeader
	Rows   []collectionPlanRow
}

type collectionPlanHeader struct {
	OperationID, UploadID, Actor                       string
	IdentityFormat, ContentDigest, InputProgressDigest string
	ItemCount, ObservedIndex                           uint64
}

type collectionPlanRow struct {
	Ordinal, InputOrdinal uint64
	Source                string
	Document, Item        uint64
	Key                   persistence.CatalogKey
	Change                string
	Target                collectionPlanGuard
	Guards                []collectionPlanGuard
	// Requires is sorted and includes unchanged transitive input dependencies.
	// A failed included dependency cannot fall back to its old live version.
	Requires []uint64
	// Touches is the sorted union of old and new reference keys, including
	// unchanged edges. A future executor may substitute only unique reverse
	// versions returned atomically by successful earlier own mutations. A Raft
	// log index is insufficient: multiple mutations can share one log entry.
	Touches []persistence.CatalogKey
}

type collectionPlanGuard struct {
	Key                           persistence.CatalogKey
	OriginalUID, OriginalRevision string
	OriginalGeneration            int64
	Absent                        bool
	// FromOrdinal requires that exact earlier row's successful receipt. It
	// supplies the current UID/revision; the original tuple remains frozen.
	// When zero, compare the original tuple, or create-if-absent for Target.
	FromOrdinal uint64
	// Non-nil guards the reverse-edge version, including zero. A later
	// executor can substitute only its own earlier successful Touches results,
	// never the currently observed version of an outside dependent edit.
	ReverseVersion *uint64
}

const (
	collectionPlanMetadataBytes = 32 << 20
	collectionPlanVisits        = 100_000
)

// Limits account conservatively for retained rows, maps, slices and sorting
// scratch before allocation. They are separate from the validator's graph and
// borrowed-plaintext budgets; they do not represent a process RSS guarantee.
type collectionPlanCompiler struct {
	plan                collectionPlan
	sources             []stagedItemRef
	ordinals            map[persistence.CatalogKey]uint64
	bytes, visits       int
	maxBytes, maxVisits int
}

// compileStagedCollectionPlan returns no plan unless whole-inventory, final
// union, every ordered prefix, and final captured-version checks all succeed.
// The public CollectionValidation shape and ordinary ephemeral path are intact.
func (c *Catalog) compileStagedCollectionPlan(ctx context.Context, captured ReadView, source *collectionValidationSource, options CollectionValidationOptions) (CollectionValidation, *collectionPlan, error) {
	if ctx == nil || source == nil || source.catalog != c || captured.catalog != c || options.CanRead == nil {
		return CollectionValidation{}, nil, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return CollectionValidation{}, nil, err
	}
	head := source.head
	p := collectionPlanCompiler{maxBytes: collectionPlanMetadataBytes, maxVisits: collectionPlanVisits,
		plan: collectionPlan{Header: collectionPlanHeader{OperationID: head.ID, UploadID: head.UploadID, Actor: head.Actor,
			IdentityFormat: head.IdentityFormat, ContentDigest: head.ContentDigest, InputProgressDigest: head.ProgressDigest,
			ItemCount: head.ItemCount, ObservedIndex: captured.Index()}}}
	if err := p.charge(2048 + len(head.ID) + len(head.UploadID) + len(head.Actor) + len(head.IdentityFormat) + len(head.ContentDigest) + len(head.ProgressDigest)); err != nil {
		return CollectionValidation{}, nil, err
	}
	result, err := c.validateStagedCollectionWithPlan(ctx, captured, source, options, &p)
	if err != nil || !result.Valid {
		return result, nil, err
	}
	if uint64(len(p.plan.Rows)) != head.ItemCount {
		return CollectionValidation{}, nil, ErrValidation
	}
	return result, &p.plan, nil
}

func (p *collectionPlanCompiler) charge(n int) error {
	if n < 0 || p.bytes < 0 || p.bytes > p.maxBytes || n > p.maxBytes-p.bytes {
		return ErrGraphLimit
	}
	p.bytes += n
	return nil
}

func (p *collectionPlanCompiler) visit(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.visits >= p.maxVisits {
		return ErrGraphLimit
	}
	p.visits++
	return nil
}

func (p *collectionPlanCompiler) source(ctx context.Context, ref stagedItemRef) error {
	if err := p.visit(ctx); err != nil {
		return err
	}
	if ref.Ordinal != uint64(len(p.sources))+1 {
		return ErrValidation
	}
	if err := p.charge(512 + len(ref.Key.Kind) + len(ref.Key.ID) + len(ref.Source)); err != nil {
		return err
	}
	p.sources = append(p.sources, ref)
	return nil
}

func (p *collectionPlanCompiler) order(ctx context.Context, order []persistence.CatalogKey) error {
	if p.ordinals != nil || len(order) != len(p.sources) {
		return ErrValidation
	}
	if err := p.charge(256); err != nil {
		return err
	}
	p.ordinals = map[persistence.CatalogKey]uint64{}
	for i, key := range order {
		if err := p.visit(ctx); err != nil {
			return err
		}
		if _, exists := p.ordinals[key]; exists {
			return ErrValidation
		}
		if err := p.charge(256 + len(key.Kind) + len(key.ID)); err != nil {
			return err
		}
		p.ordinals[key] = uint64(i + 1)
	}
	return nil
}

func (p *collectionPlanCompiler) prefix(ctx context.Context, v *stagedValidator, key persistence.CatalogKey, selected, affected map[persistence.CatalogKey]bool) error {
	if err := p.visit(ctx); err != nil {
		return err
	}
	ordinal, exists := p.ordinals[key]
	i, inputExists := v.item[key]
	if !exists || ordinal != uint64(len(p.plan.Rows))+1 || !inputExists || i < 0 || i >= len(p.sources) || p.sources[i].Key != key {
		return ErrValidation
	}
	if err := p.charge(1536 + len(key.Kind) + len(key.ID) + len(p.sources[i].Source)); err != nil {
		return err
	}
	ref := p.sources[i]
	row := collectionPlanRow{Ordinal: ordinal, InputOrdinal: ref.Ordinal, Source: ref.Source, Document: ref.Document, Item: ref.Item,
		Key: key, Change: v.result.Items[i].Change, Target: originalPlanGuard(v, key, true)}
	if row.Change != "create" && row.Change != "update" && row.Change != "unchanged" {
		return ErrValidation
	}
	requires := map[uint64]bool{}
	require := func(previous uint64) error {
		if err := p.visit(ctx); err != nil {
			return err
		}
		if previous == 0 || previous >= ordinal {
			return ErrValidation
		}
		if !requires[previous] {
			if err := p.charge(192); err != nil {
				return err
			}
			requires[previous] = true
		}
		return nil
	}
	// Sorting scratch has a bound before collectionKeys allocates its slice.
	if len(selected) > p.maxBytes/64 {
		return ErrGraphLimit
	}
	if err := p.charge(64 * len(selected)); err != nil {
		return err
	}
	for _, other := range collectionKeys(selected) {
		if err := p.visit(ctx); err != nil {
			return err
		}
		if other == key {
			continue // Target always compares the original, never itself.
		}
		if err := p.charge(768 + len(other.Kind) + len(other.ID)); err != nil {
			return err
		}
		guard := originalPlanGuard(v, other, affected[other])
		if previous, included := p.ordinals[other]; included && previous < ordinal {
			// A transitive unchanged input still requires its own successful
			// conditional observation. Existing bytes cannot replace that result.
			if err := require(previous); err != nil {
				return err
			}
		}
		if selected[other] {
			guard.FromOrdinal = p.ordinals[other]
			if err := require(guard.FromOrdinal); err != nil {
				return err
			}
		} else if guard.Absent {
			return ErrValidation
		}
		row.Guards = append(row.Guards, guard)
	}
	for _, dependency := range v.desired[key].refs {
		if err := p.visit(ctx); err != nil {
			return err
		}
		if previous, included := p.ordinals[dependency]; included {
			if err := require(previous); err != nil {
				return err
			}
		}
	}
	for previous := range requires {
		if err := p.visit(ctx); err != nil {
			return err
		}
		row.Requires = append(row.Requires, previous) // Map-entry charge includes this slice.
	}
	slices.Sort(row.Requires)
	if row.Change != "unchanged" {
		// Merge the already sorted reference lists without an unbounded scratch
		// map or retaining either validator-owned slice in the resulting plan.
		old, next := v.live[key].refs, v.desired[key].refs
		for len(old) != 0 || len(next) != 0 {
			if err := p.visit(ctx); err != nil {
				return err
			}
			var target persistence.CatalogKey
			switch {
			case len(next) == 0 || len(old) != 0 && collectionCompareKey(old[0], next[0]) < 0:
				target, old = old[0], old[1:]
			case len(old) == 0 || collectionCompareKey(old[0], next[0]) > 0:
				target, next = next[0], next[1:]
			default:
				target, old, next = old[0], old[1:], next[1:]
			}
			if len(row.Touches) != 0 && row.Touches[len(row.Touches)-1] == target {
				continue
			}
			if err := p.charge(256 + len(target.Kind) + len(target.ID)); err != nil {
				return err
			}
			row.Touches = append(row.Touches, target)
		}
	}
	p.plan.Rows = append(p.plan.Rows, row)
	return nil
}

func originalPlanGuard(v *stagedValidator, key persistence.CatalogKey, reverse bool) collectionPlanGuard {
	old, exists := v.live[key]
	guard := collectionPlanGuard{Key: key, OriginalUID: old.uid, OriginalRevision: old.revision, OriginalGeneration: old.generation, Absent: !exists}
	if exists && reverse {
		version := old.dependentsVersion
		guard.ReverseVersion = &version
	}
	return guard
}
