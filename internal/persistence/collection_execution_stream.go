package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"io"
)

var collectionExecutionLedgerMagic = []byte("CPRA-COLLECTION-EXECUTION-LEDGER\x00\x01")

// writeCollectionExecutionLedger freezes neither storage nor application state.
// Its caller supplies the same already-frozen view as the other namespaces.
// The stream accounts for physical logical bytes separately from permanent
// child-completion capacity charges. It does not export bbolt free pages.
func writeCollectionExecutionLedger(w io.Writer, view *collectionLedgerView) (int64, error) {
	if w == nil || view == nil {
		return 0, ErrCollectionUnavailable
	}
	var written, encoded, charged int64
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
	if err := write(collectionExecutionLedgerMagic); err != nil {
		return written, err
	}
	err := view.WalkExecution(context.Background(), func(record collectionExecutionRecord) error {
		raw, err := collectionExecutionEncoding(record)
		if err != nil {
			return err
		}
		charge := collectionExecutionCharge(record, raw)
		if int64(len(raw)) > maxCollectionLedgerBytes-encoded || charge > maxCollectionLedgerBytes-charged {
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
		charged += charge
		count++
		return nil
	})
	if err != nil {
		return written, err
	}
	var footer [4 + 8 + 8 + 8 + sha256.Size]byte
	binary.BigEndian.PutUint64(footer[4:12], count)
	binary.BigEndian.PutUint64(footer[12:20], uint64(encoded))
	binary.BigEndian.PutUint64(footer[20:28], uint64(charged))
	copy(footer[28:], digest.Sum(nil))
	err = write(footer[:])
	return written, err
}

// importCollectionExecutionLedger populates only a fresh, unpublished
// generation. The caller must discard it on any error, including an invalid
// footer after earlier complete batches. Consumed prepared rows are absent from
// snapshots; import therefore has its own strict primitive rather than
// pretending that a snapshot re-executes item acceptance.
func importCollectionExecutionLedger(r io.Reader, destination *collectionLedger) error {
	return importCollectionExecutionLedgerWithRetirement(r, destination, nil)
}

// importCollectionExecutionLedgerWithRetirement keeps the existing wire stream.
// Only validated recovery headers may supply original retired ordinal offsets;
// absent entries retain the strict legacy zero-prefix import behavior.
func importCollectionExecutionLedgerWithRetirement(r io.Reader, destination *collectionLedger, offsets map[string]uint64) error {
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
	err = view.WalkExecution(context.Background(), func(collectionExecutionRecord) error {
		return errCollectionLedgerCorrupt
	})
	closeErr := view.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	magic := make([]byte, len(collectionExecutionLedgerMagic))
	if _, err := io.ReadFull(r, magic); err != nil {
		return err
	}
	if !bytes.Equal(magic, collectionExecutionLedgerMagic) {
		return errCollectionLedgerCorrupt
	}
	var count uint64
	var encoded, charged, batchBytes int64
	var previousOperation, previousSlot string
	digest := sha256.New()
	batch := make([]collectionExecutionRecord, 0, collectionLedgerBatchLimit)
	defer func() { clear(batch) }()
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := destination.importRetiredExecutionBatch(batch, offsets); err != nil {
			return err
		}
		clear(batch)
		batch, batchBytes = batch[:0], 0
		return nil
	}
	for {
		var size [4]byte
		if _, err := io.ReadFull(r, size[:]); err != nil {
			return err
		}
		length := binary.BigEndian.Uint32(size[:])
		if length == 0 {
			break
		}
		if length > collectionExecutionMaxFrame || int64(length) > maxCollectionLedgerBytes-encoded {
			return errCollectionLedgerCorrupt
		}
		raw := make([]byte, int(length))
		if _, err := io.ReadFull(r, raw); err != nil {
			return err
		}
		record, err := decodeCollectionExecutionRecord(raw)
		if err != nil {
			return err
		}
		operation, slot, err := record.identity()
		if err != nil || operation < previousOperation || operation == previousOperation && slot <= previousSlot {
			return errCollectionLedgerCorrupt
		}
		charge := collectionExecutionCharge(record, raw)
		if charge > destination.maxBytes-base-charged {
			return errCollectionLedgerQuota
		}
		if len(batch) > 0 && (len(batch) == collectionLedgerBatchLimit || int64(length) > collectionLedgerBatchBytes-batchBytes) {
			if err := flush(); err != nil {
				return err
			}
		}
		batch = append(batch, record)
		batchBytes += int64(length)
		previousOperation, previousSlot = operation, slot
		_, _ = digest.Write(size[:])
		_, _ = digest.Write(raw)
		encoded += int64(length)
		charged += charge
		count++
	}
	var footer [8 + 8 + 8 + sha256.Size]byte
	if _, err := io.ReadFull(r, footer[:]); err != nil {
		return err
	}
	if binary.BigEndian.Uint64(footer[:8]) != count || binary.BigEndian.Uint64(footer[8:16]) != uint64(encoded) ||
		binary.BigEndian.Uint64(footer[16:24]) != uint64(charged) || !bytes.Equal(footer[24:], digest.Sum(nil)) {
		return errCollectionLedgerCorrupt
	}
	return flush()
}
