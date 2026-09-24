package persistence

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/hashicorp/raft"
)

const collectionSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-3\n"
const catalogMutationSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-4\n"

func validateCollectionHeaders(i image) error {
	if err := validateCollectionAdmissions(i); err != nil {
		return err
	}
	if len(i.Collections) > maxCollectionOperations || len(i.Collections) != 0 && !collectionFormat(i.Version) {
		return ErrCollectionInvalid
	}
	uploads := map[string]bool{}
	var used int64
	for id, state := range i.Collections {
		if state.NormalizationProfile != "" && i.Version < CollectionReselectionFormatVersion {
			return ErrCollectionInvalid
		}
		if state.ExecutionRetirement != nil && i.Version != CollectionExecutionRetirementFormatVersion && i.Version != CollectionExecutionSourceRetirementFormatVersion && i.Version != CollectionReselectionFormatVersion && !externalStorageFormat(i.Version) {
			return ErrCollectionInvalid
		}
		if state.ExecutionRetirement != nil && state.ExecutionRetirement.Sources != nil && i.Version != CollectionExecutionSourceRetirementFormatVersion && i.Version != CollectionReselectionFormatVersion && !externalStorageFormat(i.Version) {
			return ErrCollectionInvalid
		}
		if state.Execution != nil && !collectionExecutionStorageFormat(i.Version) {
			return ErrCollectionInvalid
		}
		if state.ExecutionResult != nil && (!collectionExecutionResultStorageFormat(i.Version) || state.ExecutionResult.hasPublication() && !collectionExecutionPublicationStorageFormat(i.Version)) {
			return ErrCollectionInvalid
		}
		if state.validate() != nil || id != state.ID || uploads[state.UploadID] {
			return ErrCollectionInvalid
		}
		if state.Owner != nil && !collectionValidationStorageFormat(i.Version) {
			return ErrCollectionInvalid
		}
		if state.Activation != nil && !collectionActivationStorageFormat(i.Version) {
			return ErrCollectionInvalid
		}
		if state.ValidationRequest != nil && !collectionValidationRequestStorageFormat(i.Version) {
			return ErrCollectionInvalid
		}
		uploads[state.UploadID] = true
		epoch, seq, _ := ParseOperationHandle(id)
		resetting := i.Restore != nil && i.Restore.Phase != "complete"
		// Restore fences old handles through the epoch without rewriting a
		// terminal outcome. Only inactive terminal inventories may carry an
		// older epoch; none can be uploaded, read for validation, or resumed.
		if !resetting && (epoch == i.OperationEpoch && seq > i.OperationHighWater ||
			collectionLive(state.Phase) && epoch != i.OperationEpoch) {
			return ErrCollectionInvalid
		}
		if _, exists := i.Operations[id]; exists {
			return ErrCollectionInvalid
		}
		if _, exists := i.OperationReservations[id]; exists {
			return ErrCollectionInvalid
		}
		remaining := state.EncodedBytes - state.RemovedBytes
		if remaining > maxCollectionLedgerBytes-used {
			return ErrCollectionQuota
		}
		used += remaining
		if state.Plan != nil {
			if !collectionPlanStorageFormat(i.Version) || state.Plan.Header.ObservedIndex > i.Index {
				return ErrCollectionInvalid
			}
			remaining = state.Plan.EncodedBytes - state.Plan.RemovedBytes
			if remaining > maxCollectionLedgerBytes-used {
				return ErrCollectionQuota
			}
			used += remaining
		}
		if state.Validation != nil {
			if !collectionValidationStorageFormat(i.Version) {
				return ErrCollectionInvalid
			}
			remaining = state.Validation.EncodedBytes - state.Validation.RemovedBytes
			if remaining > maxCollectionLedgerBytes-used {
				return ErrCollectionQuota
			}
			used += remaining
		}
		if state.Execution != nil {
			remaining, err := collectionExecutionRemainingCharge(state)
			if err != nil {
				return err
			}
			if remaining > maxCollectionLedgerBytes-used {
				return ErrCollectionQuota
			}
			used += remaining
		}
	}
	return nil
}

func validateCollectionRows(i image, view *collectionLedgerView) error {
	if err := validateCollectionHeaders(i); err != nil {
		return err
	}
	if view == nil {
		return ErrCollectionUnavailable
	}
	type progress struct {
		count  uint64
		bytes  int64
		digest string
	}
	actual := map[string]progress{}
	err := view.Walk(func(id string, item CollectionItem) error {
		state, exists := i.Collections[id]
		p := actual[id]
		if !exists || p.count >= state.Uploaded-state.RemovedRows || item.Ordinal != p.count+1 {
			return ErrCollectionInvalid
		}
		if p.count == 0 {
			p.digest = collectionInitialDigest()
		}
		cost, err := collectionItemCost(id, item)
		if err != nil || cost > state.EncodedBytes-state.RemovedBytes-p.bytes {
			return ErrCollectionInvalid
		}
		p.digest, err = collectionNextDigest(p.digest, item)
		if err != nil {
			return err
		}
		p.count++
		p.bytes += cost
		actual[id] = p
		return nil
	})
	if err != nil {
		return err
	}
	for id, state := range i.Collections {
		p := actual[id]
		if p.count == 0 {
			p.digest = collectionInitialDigest()
		}
		// A terminal upload can only lose a bounded tail. Its original final
		// digest remains audit identity, not the digest of a shortened prefix.
		// No terminal state can become executable again. The snapshot's stream
		// footer and Raft checksum still cover every retained encrypted row.
		if p.count != state.Uploaded-state.RemovedRows || p.bytes != state.EncodedBytes-state.RemovedBytes ||
			state.RemovedRows == 0 && p.digest != state.ProgressDigest {
			return ErrCollectionInvalid
		}
	}
	if err := validateCollectionPlanRows(i, view); err != nil {
		return err
	}
	if err := validateCollectionValidationRows(context.Background(), i, view); err != nil {
		return err
	}
	if collectionExecutionStorageFormat(i.Version) {
		_, err := validateCollectionExecutionInventory(context.Background(), i, view)
		return err
	}
	// Formats 3–8 have no execution namespace. Never silently discard one while
	// exporting an otherwise valid older image.
	return view.WalkExecution(context.Background(), func(collectionExecutionRecord) error {
		return ErrCollectionInvalid
	})
}

func (s *frozenSnapshot) persistCollections(sink raft.SnapshotSink) error {
	if err := validateCollectionRows(s.image, s.collections); err != nil {
		_ = sink.Cancel()
		return err
	}
	magic := collectionSnapshotMagic
	if external := externalSnapshotMagic(s.image.Version); external != "" {
		magic = external
	} else if s.image.Version == CollectionReselectionFormatVersion {
		magic = collectionReselectionSnapshotMagic
	} else if s.image.Version == CollectionExecutionSourceRetirementFormatVersion {
		magic = collectionExecutionSourceRetirementSnapshotMagic
	} else if s.image.Version == CollectionExecutionRetirementFormatVersion {
		magic = collectionExecutionRetirementSnapshotMagic
	} else if s.image.Version == CollectionExecutionPublicationFormatVersion {
		magic = collectionExecutionPublicationSnapshotMagic
	} else if s.image.Version == CollectionExecutionResultFormatVersion {
		magic = collectionExecutionResultSnapshotMagic
	} else if s.image.Version == CollectionExecutionFormatVersion {
		magic = collectionExecutionSnapshotMagic
	} else if s.image.Version == CollectionActivationFormatVersion {
		magic = collectionActivationSnapshotMagic
	} else if s.image.Version == CollectionValidationRequestFormatVersion {
		magic = collectionValidationRequestSnapshotMagic
	} else if s.image.Version == CollectionValidationFormatVersion {
		magic = collectionValidationSnapshotMagic
	} else if s.image.Version == CollectionPlanFormatVersion {
		magic = collectionPlanSnapshotMagic
	} else if s.image.Version == CatalogMutationFormatVersion {
		magic = catalogMutationSnapshotMagic
	}
	if _, err := io.WriteString(sink, magic); err != nil {
		_ = sink.Cancel()
		return err
	}
	if err := json.NewEncoder(sink).Encode(s.image); err != nil {
		_ = sink.Cancel()
		return err
	}
	if _, err := s.collections.WriteTo(sink); err != nil {
		_ = sink.Cancel()
		return err
	}
	if collectionPlanStorageFormat(s.image.Version) {
		if _, err := writeCollectionPlanLedger(sink, s.collections); err != nil {
			_ = sink.Cancel()
			return err
		}
	}
	if collectionValidationStorageFormat(s.image.Version) {
		if _, err := writeCollectionValidationLedger(sink, s.collections); err != nil {
			_ = sink.Cancel()
			return err
		}
	}
	if collectionExecutionStorageFormat(s.image.Version) {
		if _, err := writeCollectionExecutionLedger(sink, s.collections); err != nil {
			_ = sink.Cancel()
			return err
		}
	}
	if err := sink.Close(); err != nil {
		_ = sink.Cancel()
		return err
	}
	return nil
}

// decodeSnapshot accepts legacy JSON or a self-contained streamed collection
// image. It builds a fresh unpublished materialization; failure closes it and
// leaves every previous generation and authoritative snapshot/log untouched.
func decodeSnapshot(reader io.Reader, directory string) (_ image, ledger *collectionLedger, resultErr error) {
	r := bufio.NewReader(reader)
	prefix, _ := r.Peek(len(collectionExecutionPublicationSnapshotMagic))
	magicLength := len(collectionSnapshotMagic)
	version, externalLength := externalSnapshotVersion(prefix)
	switch {
	case version != 0:
		magicLength = externalLength
	case bytes.HasPrefix(prefix, []byte(collectionReselectionSnapshotMagic)):
		version = CollectionReselectionFormatVersion
		magicLength = len(collectionReselectionSnapshotMagic)
	case bytes.HasPrefix(prefix, []byte(collectionExecutionSourceRetirementSnapshotMagic)):
		version = CollectionExecutionSourceRetirementFormatVersion
		magicLength = len(collectionExecutionSourceRetirementSnapshotMagic)
	case bytes.HasPrefix(prefix, []byte(collectionExecutionRetirementSnapshotMagic)):
		version = CollectionExecutionRetirementFormatVersion
		magicLength = len(collectionExecutionRetirementSnapshotMagic)
	case bytes.HasPrefix(prefix, []byte(collectionExecutionPublicationSnapshotMagic)):
		version = CollectionExecutionPublicationFormatVersion
		magicLength = len(collectionExecutionPublicationSnapshotMagic)
	case bytes.HasPrefix(prefix, []byte(collectionExecutionResultSnapshotMagic)):
		version = CollectionExecutionResultFormatVersion
		magicLength = len(collectionExecutionResultSnapshotMagic)
	case bytes.HasPrefix(prefix, []byte(collectionSnapshotMagic)):
		version = CollectionFormatVersion
	case bytes.HasPrefix(prefix, []byte(catalogMutationSnapshotMagic)):
		version = CatalogMutationFormatVersion
	case bytes.HasPrefix(prefix, []byte(collectionPlanSnapshotMagic)):
		version = CollectionPlanFormatVersion
	case bytes.HasPrefix(prefix, []byte(collectionValidationSnapshotMagic)):
		version = CollectionValidationFormatVersion
	case bytes.HasPrefix(prefix, []byte(collectionActivationSnapshotMagic)):
		version = CollectionActivationFormatVersion
	case bytes.HasPrefix(prefix, []byte(collectionValidationRequestSnapshotMagic)):
		version = CollectionValidationRequestFormatVersion
	case bytes.HasPrefix(prefix, []byte(collectionExecutionSnapshotMagic)):
		version = CollectionExecutionFormatVersion
	}
	if version == 0 {
		i, err := decodeImage(r)
		if err == nil && collectionFormat(i.Version) {
			err = ErrCollectionInvalid // Streamed images require their complete ledger.
		}
		return i, nil, err
	}
	_, _ = r.Discard(magicLength)
	d := json.NewDecoder(r)
	d.DisallowUnknownFields()
	i, err := decodeImageValue(d)
	if err != nil || i.Version != version {
		return i, nil, errors.Join(err, ErrCollectionInvalid)
	}
	remainder := bufio.NewReader(io.MultiReader(d.Buffered(), r))
	if delimiter, err := remainder.ReadByte(); err != nil || delimiter != '\n' {
		return i, nil, ErrCollectionInvalid
	}
	if directory == "" {
		ledger, err = newMemoryCollectionLedger(maxCollectionLedgerBytes)
	} else {
		ledger, err = openCollectionLedger(directory, maxCollectionLedgerBytes)
	}
	if err != nil {
		return i, nil, err
	}
	defer func() {
		if resultErr != nil {
			_ = ledger.Close()
		}
	}()
	if err := importCollectionLedger(remainder, ledger); err != nil {
		return i, ledger, err
	}
	if collectionPlanStorageFormat(i.Version) {
		if err := importCollectionPlanLedger(remainder, ledger); err != nil {
			return i, ledger, err
		}
	}
	if collectionValidationStorageFormat(i.Version) {
		if err := importCollectionValidationLedger(remainder, ledger); err != nil {
			return i, ledger, err
		}
	}
	if collectionExecutionStorageFormat(i.Version) {
		offsets := map[string]uint64{}
		if i.Version == CollectionExecutionRetirementFormatVersion || i.Version == CollectionExecutionSourceRetirementFormatVersion || i.Version == CollectionReselectionFormatVersion || externalStorageFormat(i.Version) {
			for id, state := range i.Collections {
				if r := state.ExecutionRetirement; r != nil && r.Checkpoint != nil {
					offsets[id] = r.Checkpoint.Progress.Processed
				}
			}
		}
		if err := importCollectionExecutionLedgerWithRetirement(remainder, ledger, offsets); err != nil {
			return i, ledger, err
		}
	}
	if _, err := remainder.ReadByte(); err != io.EOF {
		return i, ledger, ErrCollectionInvalid
	}
	view, err := ledger.Freeze()
	if err != nil {
		return i, ledger, err
	}
	err = validateCollectionRows(i, view)
	err = errors.Join(err, view.Close())
	return i, ledger, err
}

// An image without staging rows still needs every namespace in its format.
// Freeze a detached empty view so
// its snapshot has the same complete framing without creating a disk cache.
// The caller holds f.mu; no mutable state or write transaction escapes it.
func (f *machine) collectionSnapshotView() (*collectionLedgerView, error) {
	if f.collections != nil {
		return f.collections.Freeze()
	}
	if len(f.image.Collections) != 0 {
		return nil, ErrCollectionUnavailable
	}
	empty, err := newMemoryCollectionLedger(maxCollectionLedgerBytes)
	if err != nil {
		return nil, err
	}
	defer empty.Close()
	return empty.Freeze()
}
