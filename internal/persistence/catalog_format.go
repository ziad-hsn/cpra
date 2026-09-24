package persistence

import (
	"errors"
	"math"
)

// CatalogMutationFormatVersion assigns a unique reverse-dependent token to
// every accepted catalog mutation, including mutations sharing a Raft entry.
// Formats 2 and 3 retain their original log-index semantics during replay.
const CatalogMutationFormatVersion = 4

func catalogMutationFormat(version int) bool {
	return version == CatalogMutationFormatVersion || collectionPlanStorageFormat(version)
}

var (
	ErrCatalogFormatDowngrade   = errors.New("catalog mutation requires the current durable format")
	ErrCatalogSequenceExhausted = errors.New("catalog mutation sequence exhausted")
)

func mutatesCatalog(c Command) bool {
	return c.CollectionExecute != nil && (c.CollectionExecute.Action == "begin" || c.CollectionExecute.Action == "prepare" || c.CollectionExecute.Action == "decide") || c.Catalog != nil || c.Bootstrap != nil && c.Bootstrap.Action == "seed"
}

// commandWriteFormat upgrades new catalog writes while retaining their original
// format-2/3 replay contract.
func commandWriteFormat(c Command) int {
	version := commandMinimumFormat(c)
	// Generic cleanup cannot retire execution-bearing parents. Their source
	// cleanup uses retire_sources; historical format8/9 keeps its replay semantics.
	if c.Collection != nil && c.Collection.Action == "cleanup" && c.Collection.Cleanup != nil && c.Collection.Cleanup.Activation != nil {
		version = CollectionExecutionResultFormatVersion
	}
	// Manifest checks historically carried this fence in format 1. Preserve
	// their replay contract, but mark all new fenced writes as format 2.
	if c.CheckControlRevision != "" {
		version = max(version, CatalogFormatVersion)
	}
	if mutatesCatalog(c) {
		version = max(version, CatalogMutationFormatVersion)
	}
	return version
}

// commandMinimumFormat is shared by writers and replay validation. Only the
// supported envelope versions are admitted; this does not accept future formats.
func commandMinimumFormat(c Command) int {
	if version := commandExtensionMinimumFormat(c); version != FormatVersion {
		return version
	}
	if collectionReselectionCommand(c) {
		return CollectionReselectionFormatVersion
	}
	if c.CollectionExecute != nil {
		if c.CollectionExecute.Action == "retire_sources" || c.CollectionExecute.SourceRetirement != nil || c.CollectionExecute.Retirement != nil && c.CollectionExecute.Retirement.Retirement != nil && c.CollectionExecute.Retirement.Retirement.Sources != nil {
			return CollectionExecutionSourceRetirementFormatVersion
		}
		if c.CollectionExecute.Action == "retire" || c.CollectionExecute.Retirement != nil {
			return CollectionExecutionRetirementFormatVersion
		}
		if c.CollectionExecute.Action == "publish" {
			return CollectionExecutionPublicationFormatVersion
		}
		if c.CollectionExecute.Action == "finalize" {
			return CollectionExecutionResultFormatVersion
		}
		return CollectionExecutionFormatVersion
	}
	if c.Authentication != nil && c.Authentication.Version == AuthenticationLifecycleFormatVersion {
		return CollectionValidationFormatVersion
	}
	if c.Collection != nil {
		if collectionActivationCommand(*c.Collection) {
			return CollectionActivationFormatVersion
		}
		if collectionValidationRequestCommand(*c.Collection) {
			return CollectionValidationRequestFormatVersion
		}
		if collectionValidationCommand(*c.Collection) || c.Collection.Create != nil && c.Collection.Create.Owner != nil {
			return CollectionValidationFormatVersion
		}
		if collectionPlanCommand(*c.Collection) {
			return CollectionPlanFormatVersion
		}
		return CollectionFormatVersion
	}
	if c.OperationAllocation != nil || c.Kind == "operation_expire" || c.Authentication != nil || c.Restore != nil || c.Operation != nil || c.Catalog != nil || c.Guard != nil || c.Bootstrap != nil || c.Control != nil || c.ManualRecovery != nil || c.ActionReview != nil || c.ExecutorSession != "" {
		return CatalogFormatVersion
	}
	return FormatVersion
}

// nextCatalogMutationSequence does not mutate state. Call after preconditions,
// before the first mutation. The previous image index bounds every old token,
// so the first new sequence exceeds all legacy values without a fleet rewrite.
func (f *machine) nextCatalogMutationSequence(version int) (uint64, error) {
	if !catalogMutationFormat(version) {
		if catalogMutationFormat(f.image.Version) || f.image.CatalogMutationSequence != 0 {
			return 0, ErrCatalogFormatDowngrade
		}
		return 0, nil
	}
	previous := f.image.CatalogMutationSequence
	if previous == 0 {
		previous = f.image.Index
	}
	if previous == math.MaxUint64 {
		return 0, ErrCatalogSequenceExhausted
	}
	return previous + 1, nil
}
