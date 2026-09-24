package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"io"
)

// CollectionPlanFormatVersion adds the complete inactive plan-fragment namespace
// to the same frozen snapshot as its input ledger and authoritative headers.
// Format 5 keeps format-4 catalog token semantics, but can precede the first
// catalog mutation and therefore have a zero CatalogMutationSequence.
const CollectionPlanFormatVersion = 5

const collectionPlanSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-5\n"

var collectionPlanLedgerMagic = []byte("CPRA-COLLECTION-PLAN-LEDGER\x00\x01")

func collectionPlanAction(action string) bool {
	return action == "plan_begin" || action == "plan_append" || action == "plan_finalize"
}

func collectionPlanCommand(c CollectionCommand) bool {
	return collectionPlanAction(c.Action) || c.Cleanup != nil && c.Cleanup.Plan != nil
}

// writeCollectionPlanLedger exports only logical fragments from the same frozen
// transaction used by the input stream. It never opens an independent read view.
func writeCollectionPlanLedger(w io.Writer, view *collectionLedgerView) (int64, error) {
	if w == nil || view == nil {
		return 0, ErrCollectionUnavailable
	}
	var written, encoded int64
	var count uint64
	digest := sha256.New()
	write := func(raw []byte) error {
		n, err := w.Write(raw)
		written += int64(n)
		if err == nil && n != len(raw) {
			err = io.ErrShortWrite
		}
		return err
	}
	if err := write(collectionPlanLedgerMagic); err != nil {
		return written, err
	}
	err := view.WalkPlans(context.Background(), func(operation string, part CollectionPlanLedgerFragment) error {
		raw, err := collectionPlanLedgerEncoding(operation, part)
		if err != nil {
			return err
		}
		if len(raw) == 0 || len(raw) > collectionPlanLedgerMaxFrame || int64(len(raw)) > maxCollectionLedgerBytes-encoded {
			return errCollectionLedgerCorrupt
		}
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(raw)))
		if err := write(size[:]); err != nil {
			return err
		}
		if err := write(raw); err != nil {
			return err
		}
		_, _ = digest.Write(size[:])
		_, _ = digest.Write(raw)
		encoded += int64(len(raw))
		count++
		return nil
	})
	if err != nil {
		return written, err
	}
	var footer [4 + 8 + 8 + sha256.Size]byte
	binary.BigEndian.PutUint64(footer[4:12], count)
	binary.BigEndian.PutUint64(footer[12:20], uint64(encoded))
	copy(footer[20:], digest.Sum(nil))
	err = write(footer[:])
	return written, err
}

// importCollectionPlanLedger consumes exactly one mandatory namespace stream.
// Prior input rows may already exist in this fresh unpublished generation.
// Failure leaves only prior atomic batches; no caller may install that prefix.
func importCollectionPlanLedger(r io.Reader, destination *collectionLedger) error {
	if r == nil || destination == nil {
		return ErrCollectionUnavailable
	}
	base, err := destination.Bytes()
	if err != nil {
		return err
	}
	view, err := destination.Freeze()
	if err != nil {
		return err
	}
	err = view.WalkPlans(context.Background(), func(string, CollectionPlanLedgerFragment) error {
		return errCollectionLedgerCorrupt // Existing plan input must never be merged.
	})
	closeErr := view.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	magic := make([]byte, len(collectionPlanLedgerMagic))
	if _, err = io.ReadFull(r, magic); err != nil {
		return err
	}
	if !bytes.Equal(magic, collectionPlanLedgerMagic) {
		return errCollectionLedgerCorrupt
	}
	digest := sha256.New()
	var count uint64
	var total, batchBytes int64
	var previousOperation, batchOperation string
	var previousOrdinal uint64
	batch := make([]CollectionPlanLedgerFragment, 0, collectionLedgerBatchLimit)
	defer func() { clear(batch) }()
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := destination.AppendPlanFragments(batchOperation, batch); err != nil {
			return err
		}
		clear(batch)
		batch, batchBytes = batch[:0], 0
		return nil
	}
	for {
		var size [4]byte
		if _, err = io.ReadFull(r, size[:]); err != nil {
			return err
		}
		length := binary.BigEndian.Uint32(size[:])
		if length == 0 {
			break
		}
		if length > collectionPlanLedgerMaxFrame {
			return errCollectionLedgerCorrupt
		}
		if int64(length) > destination.maxBytes-base-total {
			return errCollectionLedgerQuota
		}
		raw := make([]byte, int(length))
		if _, err = io.ReadFull(r, raw); err != nil {
			return err
		}
		row, err := decodeCollectionPlanLedgerRow(raw)
		if err != nil {
			return err
		}
		if row.OperationID < previousOperation || row.OperationID == previousOperation && row.Part.Ordinal != previousOrdinal+1 || row.OperationID != previousOperation && row.Part.Ordinal != 1 {
			return errCollectionLedgerCorrupt
		}
		if len(batch) > 0 && (row.OperationID != batchOperation || len(batch) == collectionLedgerBatchLimit || int64(length) > collectionLedgerBatchBytes-batchBytes) {
			if err := flush(); err != nil {
				return err
			}
		}
		batchOperation = row.OperationID
		batch = append(batch, row.Part)
		batchBytes += int64(length)
		previousOperation, previousOrdinal = row.OperationID, row.Part.Ordinal
		_, _ = digest.Write(size[:])
		_, _ = digest.Write(raw)
		total += int64(length)
		count++
	}
	var footer [8 + 8 + sha256.Size]byte
	if _, err = io.ReadFull(r, footer[:]); err != nil {
		return err
	}
	if binary.BigEndian.Uint64(footer[:8]) != count || binary.BigEndian.Uint64(footer[8:16]) != uint64(total) || !bytes.Equal(footer[16:], digest.Sum(nil)) {
		return errCollectionLedgerCorrupt
	}
	return flush()
}
