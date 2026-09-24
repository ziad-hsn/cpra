package management

import (
	"context"
	"io"

	"github.com/ziad-hsn/cpra/internal/persistence"
)

// collectionPlanArtifact describes the exact serialization of an already
// compiled plan. It is not a persisted validation result or execution grant.
// The caller owns the plan exclusively and must retain it without mutation
// until writing finishes. No catalog read or recompilation occurs on a retry.
type collectionPlanArtifact struct {
	header     persistence.CollectionPlanHeader
	plan       *collectionPlan
	descriptor persistence.CollectionPlanDescriptor
}

// prepareCollectionPlanArtifact measures the complete intended artifact before
// any future staging admission. A resumed upload must retain this descriptor;
// recomputing it from newer catalog observations would change the approved plan.
func prepareCollectionPlanArtifact(ctx context.Context, plan *collectionPlan, planID string) (*collectionPlanArtifact, error) {
	if ctx == nil || plan == nil || plan.Header.ItemCount != uint64(len(plan.Rows)) {
		return nil, ErrValidation
	}
	head := plan.Header
	a := &collectionPlanArtifact{plan: plan, header: persistence.CollectionPlanHeader{
		CodecVersion: persistence.CollectionPlanCodecVersion, CompilerVersion: persistence.CollectionPlanCompilerVersion,
		PlanID: planID, OperationID: head.OperationID, UploadID: head.UploadID, Actor: head.Actor,
		IdentityFormat: head.IdentityFormat, ContentDigest: head.ContentDigest,
		InputProgressDigest: head.InputProgressDigest, ItemCount: head.ItemCount, ObservedIndex: head.ObservedIndex,
	}}
	descriptor, err := persistence.EncodeCollectionPlan(ctx, io.Discard, a.header, a.emit)
	if err != nil {
		return nil, err
	}
	a.descriptor = descriptor
	return a, nil
}

// writeTo streams bounded typed fragments, then compares the entire output to
// the descriptor captured at preparation. The destination is provisional until
// this method succeeds; errors cannot authorize a partial artifact. Cancellation
// is cooperative and cannot interrupt an arbitrary caller-owned blocked writer.
func (a *collectionPlanArtifact) writeTo(ctx context.Context, w io.Writer) (persistence.CollectionPlanDescriptor, error) {
	if a == nil || a.plan == nil || ctx == nil || w == nil {
		return persistence.CollectionPlanDescriptor{}, ErrValidation
	}
	descriptor, err := persistence.EncodeCollectionPlan(ctx, w, a.header, a.emit)
	if err != nil {
		return persistence.CollectionPlanDescriptor{}, err
	}
	if descriptor != a.descriptor {
		return persistence.CollectionPlanDescriptor{}, persistence.ErrCollectionConflict
	}
	return descriptor, nil
}

func (a *collectionPlanArtifact) emit(e *persistence.CollectionPlanEncoder) error {
	// The codec checks context and byte/count bounds on every fragment. Only
	// one converted guard chunk is allocated, irrespective of the row's size.
	var guards [persistence.CollectionPlanMaxChunkEntries]persistence.CollectionPlanGuard
	for _, source := range a.plan.Rows {
		row := persistence.CollectionPlanRow{Ordinal: source.Ordinal, InputOrdinal: source.InputOrdinal,
			Source: source.Source, Document: source.Document, Item: source.Item,
			Key: source.Key, Change: source.Change, Target: artifactGuard(source.Target),
			GuardCount: uint64(len(source.Guards)), RequiresCount: uint64(len(source.Requires)), TouchesCount: uint64(len(source.Touches))}
		if err := e.BeginRow(row); err != nil {
			return err
		}
		for start := 0; start < len(source.Guards); start += len(guards) {
			n := min(len(guards), len(source.Guards)-start)
			for j := range n {
				guards[j] = artifactGuard(source.Guards[start+j])
			}
			err := e.WriteGuards(guards[:n])
			clear(guards[:n])
			if err != nil {
				return err
			}
		}
		for start := 0; start < len(source.Requires); start += persistence.CollectionPlanMaxChunkEntries {
			end := min(start+persistence.CollectionPlanMaxChunkEntries, len(source.Requires))
			if err := e.WriteRequires(source.Requires[start:end]); err != nil {
				return err
			}
		}
		for start := 0; start < len(source.Touches); start += persistence.CollectionPlanMaxChunkEntries {
			end := min(start+persistence.CollectionPlanMaxChunkEntries, len(source.Touches))
			if err := e.WriteTouches(source.Touches[start:end]); err != nil {
				return err
			}
		}
		if err := e.EndRow(); err != nil {
			return err
		}
	}
	return nil
}

func artifactGuard(source collectionPlanGuard) persistence.CollectionPlanGuard {
	guard := persistence.CollectionPlanGuard{Key: source.Key, OriginalUID: source.OriginalUID,
		OriginalRevision: source.OriginalRevision, OriginalGeneration: source.OriginalGeneration,
		Absent: source.Absent, FromOrdinal: source.FromOrdinal}
	if source.ReverseVersion != nil {
		version := *source.ReverseVersion
		guard.ReverseVersion = &version
	}
	return guard
}
