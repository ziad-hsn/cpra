package persistence

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"slices"

	bolt "go.etcd.io/bbolt"
)

var actionLedgerMagic = []byte("CPRA-ACTION-LEDGER\x00\x01")

// The action stream includes only the latest row versions at the frozen FSM
// boundary. Cursors holding older evidence remain pinned to the old generation;
// their MVCC versions are not copied into a restored generation.
func writeActionLedger(w io.Writer, view *collectionLedgerView) error {
	view.mu.Lock()
	defer view.mu.Unlock()
	if view.closed {
		return errCollectionLedgerClosed
	}
	progress, err := view.actionProgress()
	if err != nil {
		return err
	}
	if progress.Sequence == 0 {
		return errActionLedger
	}
	if err := view.validateActionInventory(progress); err != nil {
		return err
	}
	hasReviews := false
	if err := view.walkReviews(func(_ []byte, _ reviewRecord) error { hasReviews = true; return nil }); err != nil {
		return err
	}
	if hasReviews {
		return writeActionLedgerV2(w, view, progress)
	}
	progress.Bytes = 0 // Recomputed for the retained rows, excluding old versions.
	header, err := json.Marshal(progress)
	if err != nil {
		return errActionLedger
	}
	write := func(data []byte) error {
		n, err := w.Write(data)
		if err == nil && n != len(data) {
			return io.ErrShortWrite
		}
		return err
	}
	if err := write(actionLedgerMagic); err != nil {
		return err
	}
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(header)))
	if err := write(size[:]); err != nil {
		return err
	}
	if err := write(header); err != nil {
		return err
	}
	hash := sha256.New()
	_, _ = hash.Write(header)
	var count, total uint64
	err = view.walkActions(func(data []byte, row actionLedgerRow) error {
		if row.Sequence > progress.Sequence || uint64(len(data)) > math.MaxUint64-total || count == math.MaxUint64 {
			return errActionLedger
		}
		binary.BigEndian.PutUint32(size[:], uint32(len(data)))
		if err := write(size[:]); err != nil {
			return err
		}
		if err := write(data); err != nil {
			return err
		}
		_, _ = hash.Write(size[:])
		_, _ = hash.Write(data)
		count++
		total += uint64(len(data))
		return nil
	})
	if err != nil {
		return err
	}
	var footer [4 + 8 + 8 + sha256.Size]byte
	binary.BigEndian.PutUint64(footer[4:12], count)
	binary.BigEndian.PutUint64(footer[12:20], total)
	copy(footer[20:], hash.Sum(nil))
	return write(footer[:])
}

func (v *collectionLedgerView) actionProgress() (actionLedgerProgress, error) {
	if v.tx != nil {
		if actionBucketCount(v.tx) == 0 {
			return actionLedgerProgress{}, nil
		}
		if actionBucketCount(v.tx) != len(actionBuckets()) {
			return actionLedgerProgress{}, errActionLedger
		}
		return actionDiskProgress(v.tx)
	}
	if v.actions != nil {
		return v.actions.progress, nil
	}
	return actionLedgerProgress{}, nil
}

func (v *collectionLedgerView) validateActionInventory(progress actionLedgerProgress) error {
	digest := newActionCommandDigest()
	err := v.walkActions(func(data []byte, row actionLedgerRow) error {
		if row.Sequence > progress.Sequence {
			return errActionLedger
		}
		if v.tx != nil {
			if !bytes.Equal(v.tx.Bucket(actionRowsBucket).Get(actionVersionKey(row.Action.ID, row.Sequence)), data) {
				return errActionLedger
			}
			for bucket, key := range actionRowKeys(row) {
				if !bytes.Equal(v.tx.Bucket([]byte(bucket)).Get([]byte(key)), []byte(row.Action.ID)) {
					return errActionLedger
				}
			}
		}
		if row.Sequence == progress.Sequence {
			return digest.add(row)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := v.walkReviews(func(_ []byte, r reviewRecord) error {
		if r.Sequence > progress.Sequence {
			return errActionLedger
		}
		if r.Sequence == progress.Sequence {
			return digest.addReview(r)
		}
		return nil
	}); err != nil {
		return err
	}
	if progress.Sequence != 0 && (digest.count == 0 || digest.sum() != progress.Digest) {
		return errActionLedger
	}
	if v.tx == nil {
		return nil
	}
	if actionBucketCount(v.tx) != len(actionBuckets()) {
		return errActionLedger
	}
	var previous actionLedgerRow
	var previousData []byte
	var total uint64
	err = v.tx.Bucket(actionRowsBucket).ForEach(func(key, data []byte) error {
		row, err := decodeActionRow(data)
		if err != nil || !bytes.Equal(key, actionVersionKey(row.Action.ID, row.Sequence)) || row.Sequence > progress.Sequence {
			return errActionLedger
		}
		if previous.Action.ID == row.Action.ID {
			if row.Previous != previous.Sequence {
				return errActionLedger
			}
		} else if previous.Action.ID != "" && !bytes.Equal(previousData, v.tx.Bucket(actionCurrentBucket).Get([]byte(previous.Action.ID))) {
			return errActionLedger
		}
		if uint64(len(data)) > math.MaxUint64-total {
			return errActionLedger
		}
		total += uint64(len(data))
		previous, previousData = row, data
		return nil
	})
	if err != nil {
		return err
	}
	if err := v.walkReviews(func(data []byte, r reviewRecord) error {
		total += uint64(len(data))
		key := v.tx.Bucket(reviewReadyBucket).Get([]byte(r.Key))
		if (!r.Deleted && !bytes.Equal(key, []byte(r.MonitorID))) || (r.Deleted && key != nil) {
			return errActionLedger
		}
		return nil
	}); err != nil {
		return err
	}
	if previous.Action.ID != "" && !bytes.Equal(previousData, v.tx.Bucket(actionCurrentBucket).Get([]byte(previous.Action.ID))) || total != progress.Bytes {
		return errActionLedger
	}
	if err := v.tx.Bucket(reviewReadyBucket).ForEach(func(key, value []byte) error {
		r, err := decodeReviewRecord(v.tx.Bucket(reviewRowsBucket).Get(key))
		if err != nil || r.Deleted || r.Key != string(key) || r.MonitorID != string(value) {
			return errActionLedger
		}
		return nil
	}); err != nil {
		return err
	}
	// Every index entry must be justified by its current row. Deletion or
	// substitution cannot become an authoritative omission in a new snapshot.
	for _, name := range [][]byte{actionMonitorBucket, actionOwnerBucket, actionEpisodeBucket, actionIncidentBucket, actionReviewBucket, actionCooldownBucket} {
		if err := v.tx.Bucket(name).ForEach(func(key, id []byte) error {
			row, err := decodeActionRow(v.tx.Bucket(actionCurrentBucket).Get(id))
			if err != nil || row.Action.ID != string(id) || actionRowKeys(row)[string(name)] != string(key) {
				return errActionLedger
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func (v *collectionLedgerView) walkActions(visit func([]byte, actionLedgerRow) error) error {
	read := func(id string, data []byte) error {
		row, err := decodeActionRow(data)
		if err != nil || row.Action.ID != id {
			return errActionLedger
		}
		return visit(data, row)
	}
	if v.tx != nil {
		bucket := v.tx.Bucket(actionCurrentBucket)
		if bucket == nil {
			return nil
		}
		return bucket.ForEach(func(id, data []byte) error { return read(string(id), data) })
	}
	if v.actions == nil {
		return nil
	}
	ids := make([]string, 0, len(v.actions.current))
	for id := range v.actions.current {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if err := read(id, v.actions.current[id]); err != nil {
			return err
		}
	}
	return nil
}

// Import into an unpublished generation only. A failure leaves it unusable;
// callers close it and keep the previous generation. Bounded synchronous
// transactions restore rows before publishing the footer-validated progress.
func importActionLedger(r io.Reader, ledger *collectionLedger) (actionLedgerProgress, error) {
	var progress actionLedgerProgress
	magic := make([]byte, len(actionLedgerMagic))
	if _, err := io.ReadFull(r, magic); err != nil {
		return progress, err
	}
	if bytes.Equal(magic, actionLedgerMagicV2) {
		return importActionLedgerV2(r, ledger)
	}
	if !bytes.Equal(magic, actionLedgerMagic) {
		return progress, errActionLedger
	}
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return progress, err
	}
	length := binary.BigEndian.Uint32(size[:])
	if length == 0 || length > 1024 {
		return progress, errActionLedger
	}
	header := make([]byte, int(length))
	if _, err := io.ReadFull(r, header); err != nil {
		return progress, err
	}
	decoder := json.NewDecoder(bytes.NewReader(header))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&progress) != nil || progress.Sequence == 0 || progress.Position.Index == 0 || progress.Bytes != 0 || len(progress.Digest) != 64 {
		return progress, errActionLedger
	}
	canonical, _ := json.Marshal(progress)
	if !bytes.Equal(header, canonical) {
		return progress, errActionLedger
	}
	if _, err := hex.DecodeString(progress.Digest); err != nil {
		return progress, errActionLedger
	}
	if err := ledger.beginActionImport(); err != nil {
		return progress, err
	}
	hash := sha256.New()
	_, _ = hash.Write(header)
	commandDigest := newActionCommandDigest()
	var count, total uint64
	previous := ""
	batch := make([][]byte, 0, collectionLedgerBatchLimit)
	batchBytes := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := ledger.importActionBatch(batch); err != nil {
			return err
		}
		clear(batch)
		batch, batchBytes = batch[:0], 0
		return nil
	}
	for {
		if _, err := io.ReadFull(r, size[:]); err != nil {
			return progress, err
		}
		length := binary.BigEndian.Uint32(size[:])
		if length == 0 {
			break
		}
		if length > actionLedgerMaxFrame || uint64(length) > math.MaxUint64-total || count == math.MaxUint64 {
			return progress, errActionLedger
		}
		data := make([]byte, int(length))
		if _, err := io.ReadFull(r, data); err != nil {
			return progress, err
		}
		row, err := decodeActionRow(data)
		if err != nil || row.Sequence > progress.Sequence || row.Action.ID <= previous {
			return progress, errActionLedger
		}
		if row.Sequence == progress.Sequence {
			if err := commandDigest.add(row); err != nil {
				return progress, err
			}
		}
		if len(batch) == collectionLedgerBatchLimit || batchBytes+len(data) > collectionLedgerBatchBytes {
			if err := flush(); err != nil {
				return progress, err
			}
		}
		batch = append(batch, data)
		batchBytes += len(data)
		previous = row.Action.ID
		count++
		total += uint64(len(data))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write(data)
	}
	var footer [8 + 8 + sha256.Size]byte
	if _, err := io.ReadFull(r, footer[:]); err != nil {
		return progress, err
	}
	if count == 0 || binary.BigEndian.Uint64(footer[:8]) != count || binary.BigEndian.Uint64(footer[8:16]) != total || !bytes.Equal(footer[16:], hash.Sum(nil)) {
		return progress, errActionLedger
	}
	if commandDigest.count == 0 || commandDigest.sum() != progress.Digest {
		return progress, errActionLedger
	}
	if err := flush(); err != nil {
		return progress, err
	}
	progress.Bytes = total
	return progress, ledger.finishActionImport(progress)
}

func (l *collectionLedger) beginActionImport() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errCollectionLedgerClosed
	}
	if l.db == nil {
		if l.actions != nil {
			return errActionLedger
		}
		l.actions = newActionLedgerMemory()
		return nil
	}
	return l.db.Update(func(tx *bolt.Tx) error {
		for _, name := range actionBuckets() {
			if _, err := tx.CreateBucket(name); err != nil {
				return errors.Join(errActionLedger, err)
			}
		}
		return nil
	})
}

func (l *collectionLedger) importActionBatch(batch [][]byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errCollectionLedgerClosed
	}
	if l.db == nil {
		for _, data := range batch {
			row, err := decodeActionRow(data)
			if err != nil {
				return err
			}
			if old := l.actions.current[row.Action.ID]; old != nil {
				prior, e := decodeActionRow(old)
				if e != nil || prior.Sequence >= row.Sequence || row.Previous != prior.Sequence {
					return errActionLedger
				}
			}
			if old := l.actions.current[row.Action.ID]; old != nil {
				prior, _ := decodeActionRow(old)
				l.actions.byCooldown.Delete(rejectionIndexKey(prior))
			}
			l.actions.current[row.Action.ID] = data
			l.actions.versions[row.Action.ID] = append(l.actions.versions[row.Action.ID], data)
			l.actions.order.ReplaceOrInsert(row.Action.ID)
			l.actions.byMonitor.ReplaceOrInsert(row.MonitorID + "\x00" + row.Action.ID)
			l.actions.byCooldown.ReplaceOrInsert(rejectionIndexKey(row))
		}
		return nil
	}
	return l.db.Update(func(tx *bolt.Tx) error {
		for _, data := range batch {
			row, err := decodeActionRow(data)
			if err != nil {
				return err
			}
			current := tx.Bucket(actionCurrentBucket)
			if current == nil {
				return errActionLedger
			}
			if old := current.Get([]byte(row.Action.ID)); old != nil {
				prior, e := decodeActionRow(old)
				if e != nil || prior.Sequence >= row.Sequence || row.Previous != prior.Sequence {
					return errActionLedger
				}
				for bucket, key := range actionRowKeys(prior) {
					if e = tx.Bucket([]byte(bucket)).Delete([]byte(key)); e != nil {
						return e
					}
				}
			}
			if err := current.Put([]byte(row.Action.ID), data); err != nil {
				return err
			}
			if err := tx.Bucket(actionRowsBucket).Put(actionVersionKey(row.Action.ID, row.Sequence), data); err != nil {
				return err
			}
			for bucket, key := range actionRowKeys(row) {
				if err := tx.Bucket([]byte(bucket)).Put([]byte(key), []byte(row.Action.ID)); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (l *collectionLedger) finishActionImport(p actionLedgerProgress) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errCollectionLedgerClosed
	}
	if l.db == nil {
		l.actions.progress = p
		return nil
	}
	data, err := json.Marshal(p)
	if err != nil {
		return errActionLedger
	}
	return l.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(actionMetaBucket).Put([]byte("progress"), data) })
}
