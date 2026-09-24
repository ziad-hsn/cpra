package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"io"
)

var collectionValidationLedgerMagic = []byte("CPRA-COLLECTION-VALIDATION-LEDGER\x00\x01")

// writeCollectionValidationLedger exports only logical validation items from the same frozen
// transaction used by the input stream. It never opens an independent read view.
func writeCollectionValidationLedger(w io.Writer, view *collectionLedgerView) (int64, error) {
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
	if err := write(collectionValidationLedgerMagic); err != nil {
		return written, err
	}
	err := view.WalkValidation(context.Background(), func(operation string, item CollectionValidationItem) error {
		raw, err := collectionValidationLedgerEncoding(operation, item)
		if err != nil {
			return err
		}
		if len(raw) == 0 || len(raw) > collectionValidationLedgerMaxFrame || int64(len(raw)) > maxCollectionLedgerBytes-encoded {
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

// importCollectionValidationLedger consumes exactly one mandatory namespace stream.
// Prior input rows may already exist in this fresh unpublished generation.
// Failure leaves only prior atomic batches; no caller may install that prefix.
func importCollectionValidationLedger(r io.Reader, destination *collectionLedger) error {
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
	err = view.WalkValidation(context.Background(), func(string, CollectionValidationItem) error {
		return errCollectionLedgerCorrupt // Existing validation input must never be merged.
	})
	closeErr := view.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	magic := make([]byte, len(collectionValidationLedgerMagic))
	if _, err = io.ReadFull(r, magic); err != nil {
		return err
	}
	if !bytes.Equal(magic, collectionValidationLedgerMagic) {
		return errCollectionLedgerCorrupt
	}
	digest := sha256.New()
	var count uint64
	var total, batchBytes int64
	var previousOperation, batchOperation string
	var previousOrdinal uint64
	batch := make([]CollectionValidationItem, 0, collectionLedgerBatchLimit)
	defer func() { clear(batch) }()
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := destination.AppendValidationItems(batchOperation, batch); err != nil {
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
		if length > collectionValidationLedgerMaxFrame {
			return errCollectionLedgerCorrupt
		}
		if int64(length) > destination.maxBytes-base-total {
			return errCollectionLedgerQuota
		}
		raw := make([]byte, int(length))
		if _, err = io.ReadFull(r, raw); err != nil {
			return err
		}
		row, err := decodeCollectionValidationLedgerRow(raw)
		if err != nil {
			return err
		}
		if row.OperationID < previousOperation || row.OperationID == previousOperation && row.Item.Ordinal != previousOrdinal+1 || row.OperationID != previousOperation && row.Item.Ordinal != 1 {
			return errCollectionLedgerCorrupt
		}
		if len(batch) > 0 && (row.OperationID != batchOperation || len(batch) == collectionLedgerBatchLimit || int64(length) > collectionLedgerBatchBytes-batchBytes) {
			if err := flush(); err != nil {
				return err
			}
		}
		batchOperation = row.OperationID
		batch = append(batch, row.Item)
		batchBytes += int64(length)
		previousOperation, previousOrdinal = row.OperationID, row.Item.Ordinal
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
