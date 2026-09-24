package persistence

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
)

var collectionLedgerMagic = []byte("CPRA-COLLECTION-LEDGER\x00\x01")

// WriteTo streams canonical logical rows, excluding bbolt free pages and local
// generation paths. Each row is bounded independently. The footer detects
// truncation/reordering of the complete framed inventory; encryption and
// the outer Raft snapshot checksum supply their separate integrity boundaries.
func (v *collectionLedgerView) WriteTo(w io.Writer) (int64, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return 0, errCollectionLedgerClosed
	}
	var written int64
	write := func(data []byte) error {
		n, err := w.Write(data)
		written += int64(n)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
		return err
	}
	if err := write(collectionLedgerMagic); err != nil {
		return written, err
	}
	digest := sha256.New()
	var count uint64
	err := v.walk(func(data []byte, _ collectionLedgerRow) error {
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(data)))
		if err := write(size[:]); err != nil {
			return err
		}
		if err := write(data); err != nil {
			return err
		}
		_, _ = digest.Write(size[:])
		_, _ = digest.Write(data)
		count++
		return nil
	})
	if err != nil {
		return written, err
	}
	var footer [4 + 8 + 8 + sha256.Size]byte
	binary.BigEndian.PutUint64(footer[4:12], count)
	binary.BigEndian.PutUint64(footer[12:20], uint64(v.bytes-v.planBytes-v.validationBytes-v.executionBytes))
	copy(footer[20:], digest.Sum(nil))
	err = write(footer[:])
	return written, err
}

// importCollectionLedger consumes exactly one framed stream into an empty,
// unpublished generation. The caller must discard that generation on failure;
// it must never install a partially restored ledger. It does not consume any
// trailing outer snapshot fields, allowing the snapshot decoder to check EOF.
// Synchronous transactions contain at most 256 rows and 4 MiB of encoded rows.
func importCollectionLedger(r io.Reader, destination *collectionLedger) error {
	used, err := destination.Bytes()
	if err != nil {
		return err
	}
	if used != 0 {
		return errors.New("collection ledger import requires an empty generation")
	}
	magic := make([]byte, len(collectionLedgerMagic))
	if _, err = io.ReadFull(r, magic); err != nil {
		return err
	}
	if !bytes.Equal(magic, collectionLedgerMagic) {
		return errCollectionLedgerCorrupt
	}
	digest := sha256.New()
	var count uint64
	var total int64
	var previousOperation string
	var previousOrdinal uint64
	var batchOperation string
	var batchBytes int64
	batch := make([]CollectionItem, 0, collectionLedgerBatchLimit)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := destination.AppendBatch(batchOperation, batch); err != nil {
			return err
		}
		clear(batch)
		batch, batchBytes = batch[:0], 0
		return nil
	}
	defer func() { clear(batch) }()
	for {
		var size [4]byte
		if _, err = io.ReadFull(r, size[:]); err != nil {
			return err
		}
		length := binary.BigEndian.Uint32(size[:])
		if length == 0 {
			break
		}
		if length > collectionLedgerMaxFrame {
			return errCollectionLedgerCorrupt
		}
		if int64(length) > destination.maxBytes-total {
			return errCollectionLedgerQuota
		}
		data := make([]byte, int(length))
		if _, err = io.ReadFull(r, data); err != nil {
			return err
		}
		row, err := decodeCollectionLedgerRow(data)
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
		_, _ = digest.Write(data)
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
