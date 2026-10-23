package persistence

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"slices"

	bolt "go.etcd.io/bbolt"
)

var actionLedgerMagicV2 = []byte("CPRA-ACTION-LEDGER\x00\x02")

// Pending facts retain their fenced action versions. With no pending facts,
// latest heads suffice; existing readers continue pinning their old generation.
func (v *collectionLedgerView) walkActionVersions(all bool, visit func([]byte, actionLedgerRow) error) error {
	if !all {
		return v.walkActions(visit)
	}
	if v.tx != nil {
		return v.tx.Bucket(actionRowsBucket).ForEach(func(_ []byte, d []byte) error {
			r, e := decodeActionRow(d)
			if e != nil {
				return e
			}
			return visit(d, r)
		})
	}
	if v.actions == nil {
		return nil
	}
	ids := make([]string, 0, len(v.actions.versions))
	for id := range v.actions.versions {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		for _, d := range v.actions.versions[id] {
			r, e := decodeActionRow(d)
			if e != nil {
				return e
			}
			if e = visit(d, r); e != nil {
				return e
			}
		}
	}
	return nil
}

// Called with the frozen view lock held. No mutable FSM state is consulted.
func writeActionLedgerV2(w io.Writer, v *collectionLedgerView, p actionLedgerProgress) error {
	retain := false
	fences := make(map[string]bool)
	if err := v.walkReviews(func(_ []byte, r reviewRecord) error {
		if r.Fact != nil && !r.Deleted {
			retain = true
			fences[r.MonitorID] = true
		}
		return nil
	}); err != nil {
		return err
	}
	p.Bytes = 0
	header, err := json.Marshal(p)
	if err != nil {
		return err
	}
	write := func(b []byte) error {
		n, e := w.Write(b)
		if e == nil && n != len(b) {
			e = io.ErrShortWrite
		}
		return e
	}
	if err = write(actionLedgerMagicV2); err != nil {
		return err
	}
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(header)))
	if err = write(size[:]); err != nil {
		return err
	}
	if err = write(header); err != nil {
		return err
	}
	hash := sha256.New()
	hash.Write(header)
	var count, total uint64
	frame := func(kind byte, data []byte) error {
		if len(data) > reviewLedgerMaxFrame || uint64(len(data)) > math.MaxUint64-total {
			return errActionLedger
		}
		blob := append([]byte{kind}, data...)
		binary.BigEndian.PutUint32(size[:], uint32(len(blob)))
		if err := write(size[:]); err != nil {
			return err
		}
		if err := write(blob); err != nil {
			return err
		}
		hash.Write(size[:])
		hash.Write(blob)
		count++
		total += uint64(len(data))
		return nil
	}
	if err = v.walkActionVersions(retain, func(d []byte, row actionLedgerRow) error {
		if !fences[row.MonitorID] {
			if retain {
				head, _, err := v.readActionHead(row.Action.ID)
				if err != nil {
					return err
				}
				if row.Sequence != head.Sequence {
					return nil
				}
			}
			if row.Deleted && row.Sequence != p.Sequence {
				return nil
			}
		}
		return frame(1, d)
	}); err != nil {
		return err
	}
	if err = v.walkReviews(func(d []byte, _ reviewRecord) error { return frame(2, d) }); err != nil {
		return err
	}
	var footer [4 + 8 + 8 + sha256.Size]byte
	binary.BigEndian.PutUint64(footer[4:12], count)
	binary.BigEndian.PutUint64(footer[12:20], total)
	copy(footer[20:], hash.Sum(nil))
	return write(footer[:])
}

func importActionLedgerV2(r io.Reader, l *collectionLedger) (actionLedgerProgress, error) {
	var p actionLedgerProgress
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return p, err
	}
	n := binary.BigEndian.Uint32(size[:])
	if n == 0 || n > 1024 {
		return p, errActionLedger
	}
	header := make([]byte, n)
	if _, err := io.ReadFull(r, header); err != nil {
		return p, err
	}
	decoder := json.NewDecoder(bytes.NewReader(header))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&p) != nil || p.Sequence == 0 || p.Position.Index == 0 || p.Bytes != 0 || len(p.Digest) != 64 {
		return p, errActionLedger
	}
	canonical, _ := json.Marshal(p)
	if !bytes.Equal(canonical, header) {
		return p, errActionLedger
	}
	if err := l.beginActionImport(); err != nil {
		return p, err
	}
	hash := sha256.New()
	hash.Write(header)
	digest := newActionCommandDigest()
	var count, total, previousSequence uint64
	previous, previousReview := "", ""
	inReviews := false
	var actions, reviews [][]byte
	used := 0
	flush := func() error {
		if len(actions) > 0 {
			if err := l.importActionBatch(actions); err != nil {
				return err
			}
		}
		if len(reviews) > 0 {
			if err := l.importReviewBatch(reviews); err != nil {
				return err
			}
		}
		actions, reviews = nil, nil
		used = 0
		return nil
	}
	for {
		if _, err := io.ReadFull(r, size[:]); err != nil {
			return p, err
		}
		n := binary.BigEndian.Uint32(size[:])
		if n == 0 {
			break
		}
		if n < 2 || n > reviewLedgerMaxFrame+1 {
			return p, errActionLedger
		}
		blob := make([]byte, n)
		if _, err := io.ReadFull(r, blob); err != nil {
			return p, err
		}
		data := blob[1:]
		if len(actions)+len(reviews) == 64 || used+len(data) > 1<<20 {
			if err := flush(); err != nil {
				return p, err
			}
		}
		switch blob[0] {
		case 1:
			row, err := decodeActionRow(data)
			if err != nil || inReviews || row.Sequence > p.Sequence || row.Action.ID < previous || row.Action.ID == previous && (row.Sequence <= previousSequence || row.Previous != previousSequence) {
				return p, errActionLedger
			}
			if row.Sequence == p.Sequence {
				if err = digest.add(row); err != nil {
					return p, err
				}
			}
			previous, previousSequence = row.Action.ID, row.Sequence
			actions = append(actions, data)
		case 2:
			inReviews = true
			row, err := decodeReviewRecord(data)
			if err != nil || row.Sequence > p.Sequence || row.Key <= previousReview {
				return p, errActionLedger
			}
			if row.Sequence == p.Sequence {
				if err = digest.addReview(row); err != nil {
					return p, err
				}
			}
			previousReview = row.Key
			reviews = append(reviews, data)
		default:
			return p, errActionLedger
		}
		if uint64(len(data)) > math.MaxUint64-total {
			return p, errActionLedger
		}
		used += len(data)
		count++
		total += uint64(len(data))
		hash.Write(size[:])
		hash.Write(blob)
	}
	var footer [8 + 8 + sha256.Size]byte
	if _, err := io.ReadFull(r, footer[:]); err != nil {
		return p, err
	}
	if count == 0 || binary.BigEndian.Uint64(footer[:8]) != count || binary.BigEndian.Uint64(footer[8:16]) != total || !bytes.Equal(footer[16:], hash.Sum(nil)) || digest.sum() != p.Digest {
		return p, errActionLedger
	}
	if err := flush(); err != nil {
		return p, err
	}
	p.Bytes = total
	return p, l.finishActionImport(p)
}

func (l *collectionLedger) importReviewBatch(batch [][]byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errCollectionLedgerClosed
	}
	if l.db != nil {
		return l.db.Update(func(tx *bolt.Tx) error {
			for _, data := range batch {
				r, e := decodeReviewRecord(data)
				if e != nil {
					return e
				}
				if tx.Bucket(reviewRowsBucket).Get([]byte(r.Key)) != nil {
					return errActionLedger
				}
				if e = putReviewRecord(tx, r, data); e != nil {
					return e
				}
			}
			return nil
		})
	}
	for _, data := range batch {
		r, e := decodeReviewRecord(data)
		if e != nil {
			return e
		}
		if l.actions.reviews[r.Key] != nil {
			return errActionLedger
		}
		l.actions.updateReviewIndexes(r)
		l.actions.reviews[r.Key] = data
		if !r.Deleted {
			l.actions.reviewOrder.ReplaceOrInsert(r.Key)
		}
	}
	return nil
}
