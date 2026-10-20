package persistence

import (
	"bytes"
	"container/heap"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

var ErrHistoryCursorExpired = errors.New("history retention advanced; start a new history view")

// HistoryView freezes a log upper bound and retention epoch, never open database
// transactions. The HTTP layer binds the view to its principal and expiry.
type HistoryView struct {
	store               *HistoryStore
	upper               string
	cutoff              time.Time
	retentionGeneration uint64
}

func (h *HistoryStore) Snapshot() (HistoryView, error) {
	return h.SnapshotContext(context.Background())
}

func (h *HistoryStore) SnapshotContext(ctx context.Context) (HistoryView, error) {
	if err := collectionReadLock(ctx, &h.mu); err != nil {
		return HistoryView{}, err
	}
	defer h.mu.RUnlock()
	if h.err != nil || h.closed {
		return HistoryView{}, ErrHistoryUnavailable
	}
	return HistoryView{store: h, upper: fmt.Sprintf("%020d:%08d", h.catalog.Index, 99999999), cutoff: h.catalog.Cutoff, retentionGeneration: h.retentionGeneration}, nil
}

const historyPageBytes = 4 << 20

// Only the next key and encoded length are retained for each daily segment.
// Log positions, rather than dates, determine global event order.
type historyHead struct {
	db       *bolt.DB
	position string
	bytes    int
}
type historyHeads []historyHead

func (h historyHeads) Len() int           { return len(h) }
func (h historyHeads) Less(i, j int) bool { return h[i].position < h[j].position }
func (h historyHeads) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *historyHeads) Push(v any)        { *h = append(*h, v.(historyHead)) }
func (h *historyHeads) Pop() any {
	n := len(*h) - 1
	v := (*h)[n]
	(*h)[n] = historyHead{}
	*h = (*h)[:n]
	return v
}
func nextHistoryHead(ctx context.Context, db *bolt.DB, prefix []byte, after, upper string) (historyHead, bool, error) {
	head := historyHead{db: db}
	err := db.View(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		bucket := tx.Bucket([]byte("events"))
		if bucket == nil {
			return ErrHistoryUnavailable
		}
		cursor := bucket.Cursor()
		key := append(bytes.Clone(prefix), []byte(after)...)
		k, raw := cursor.Seek(key)
		if bytes.Equal(k, key) {
			k, raw = cursor.Next()
		}
		if k == nil || !bytes.HasPrefix(k, prefix) {
			return nil
		}
		position := string(k[len(prefix):])
		if position > upper {
			return nil
		}
		if !validHistoryPosition(position) || len(raw) == 0 || len(raw) > actionLedgerMaxFrame {
			return ErrHistoryUnavailable
		}
		head.position, head.bytes = position, len(raw)
		return nil
	})
	return head, head.position != "", err
}

// Page bounds scanned records and encoded bytes globally across daily segments.
// It never keeps database transactions open between pages. Retention invalidates
// the view rather than silently changing a retried page.
func (v HistoryView) Page(ctx context.Context, monitorID, after string, limit int) ([]Event, string, error) {
	if v.store == nil || monitorID == "" || len(monitorID) > 256 || strings.ContainsRune(monitorID, 0) || limit < 1 || limit > 500 || len(after) > 64 || (after != "" && !validHistoryPosition(after)) {
		return nil, "", errors.New("invalid history page")
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	h := v.store
	if err := collectionReadLock(ctx, &h.mu); err != nil {
		return nil, "", err
	}
	defer h.mu.RUnlock()
	return v.pageLocked(ctx, monitorID, after, limit)
}

// pageLocked shares the bounded scan with the v1 cursor without nesting locks.
// The caller holds the history read lock for this entire page.
func (v HistoryView) pageLocked(ctx context.Context, monitorID, after string, limit int) ([]Event, string, error) {
	h := v.store
	if h.err != nil || h.closed {
		return nil, "", ErrHistoryUnavailable
	}
	if h.retentionGeneration != v.retentionGeneration {
		return nil, "", ErrHistoryCursorExpired
	}
	const scanLimit = 10000
	items := make([]Event, 0, min(limit, 100))
	used, inspected := 0, 0
	last := after
	accept := func(e Event, size int) {
		used += size
		inspected++
		last = e.ID
		if !e.At.Before(v.cutoff) {
			items = append(items, e.Clone())
		}
	}
	full := func(size int) bool {
		return len(items) == limit || inspected == scanLimit || inspected > 0 && size > historyPageBytes-used
	}
	if h.dir == "" {
		start := sort.Search(len(h.memory), func(i int) bool { return h.memory[i].ID > after })
		for i := start; i < len(h.memory); i++ {
			if err := ctx.Err(); err != nil {
				return nil, "", err
			}
			e := h.memory[i]
			if e.ID > v.upper {
				break
			}
			if len(items) == limit || inspected == scanLimit || used == historyPageBytes {
				return items, last, nil
			}
			if e.MonitorID != monitorID {
				inspected++
				last = e.ID
				continue
			}
			size := h.memorySizes[e.ID]
			if size < 1 || size > actionLedgerMaxFrame {
				return nil, "", ErrHistoryUnavailable
			}
			if full(size) {
				return items, last, nil
			}
			accept(e, size)
		}
		return items, "", nil
	}
	prefix := []byte(monitorID + "\x00")
	heads := historyHeads{}
	for day, db := range h.databases {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		if _, err := os.Stat(filepath.Join(h.dir, day+".db")); err != nil {
			return nil, "", ErrHistoryUnavailable
		}
		head, ok, err := nextHistoryHead(ctx, db, prefix, after, v.upper)
		if err != nil {
			return nil, "", err
		}
		if ok {
			heap.Push(&heads, head)
		}
	}
	for heads.Len() > 0 {
		head := heads[0]
		if head.position <= last {
			return nil, "", ErrHistoryUnavailable
		}
		if full(head.bytes) {
			return items, last, nil
		}
		heap.Pop(&heads)
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		var event Event
		err := head.db.View(func(tx *bolt.Tx) error {
			bucket := tx.Bucket([]byte("events"))
			if bucket == nil {
				return ErrHistoryUnavailable
			}
			raw := bucket.Get(append(bytes.Clone(prefix), []byte(head.position)...))
			if len(raw) != head.bytes {
				return ErrHistoryUnavailable
			}
			var err error
			event, err = decodeHistoryEvent(raw)
			if err != nil || event.ID != head.position || event.MonitorID != monitorID {
				return ErrHistoryUnavailable
			}
			return nil
		})
		if err != nil {
			return nil, "", err
		}
		accept(event, head.bytes)
		next, ok, err := nextHistoryHead(ctx, head.db, prefix, head.position, v.upper)
		if err != nil {
			return nil, "", err
		}
		if ok {
			heap.Push(&heads, next)
		}
	}
	return items, "", nil
}

func validHistoryPosition(value string) bool {
	if len(value) != 29 || value[20] != ':' {
		return false
	}
	for i, b := range []byte(value) {
		if i != 20 && (b < '0' || b > '9') {
			return false
		}
	}
	return true
}
