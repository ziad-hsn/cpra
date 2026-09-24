package persistence

import (
	"bytes"
	"container/heap"
	"context"
	"encoding/json"
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
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.err != nil || h.closed {
		return HistoryView{}, ErrHistoryUnavailable
	}
	return HistoryView{store: h, upper: fmt.Sprintf("%020d:%08d", h.catalog.Index, 99999999), cutoff: h.catalog.Cutoff, retentionGeneration: h.retentionGeneration}, nil
}

type historyCandidates []Event

func (h historyCandidates) Len() int           { return len(h) }
func (h historyCandidates) Less(i, j int) bool { return h[i].ID > h[j].ID }
func (h historyCandidates) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *historyCandidates) Push(v any)        { *h = append(*h, v.(Event)) }
func (h *historyCandidates) Pop() any {
	n := len(*h) - 1
	v := (*h)[n]
	(*h)[n] = Event{}
	*h = (*h)[:n]
	return v
}

// Page uses indexed daily segments and at most limit+1 retained candidates.
// Memory mode examines at most 10,000 records per page, with a continuation even
// for an empty page. Retention invalidates the view instead of changing retries.
func (v HistoryView) Page(ctx context.Context, monitorID, after string, limit int) ([]Event, string, error) {
	if v.store == nil || monitorID == "" || len(monitorID) > 256 || strings.ContainsRune(monitorID, 0) || limit < 1 || limit > 500 || len(after) > 64 || (after != "" && !validHistoryPosition(after)) {
		return nil, "", errors.New("invalid history page")
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	h := v.store
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.err != nil || h.closed {
		return nil, "", ErrHistoryUnavailable
	}
	if h.retentionGeneration != v.retentionGeneration {
		return nil, "", ErrHistoryCursorExpired
	}
	const scanLimit = 10000
	frontier := v.upper
	candidates := historyCandidates{}
	accept := func(e Event) {
		if e.MonitorID != monitorID || e.ID <= after || e.ID > v.upper || e.At.Before(v.cutoff) {
			return
		}
		e = e.Clone()
		heap.Push(&candidates, e)
		if candidates.Len() > limit+1 {
			heap.Pop(&candidates)
		}
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
			accept(e)
			if i-start+1 == scanLimit && i+1 < len(h.memory) && h.memory[i+1].ID <= v.upper {
				frontier = e.ID
				break
			}
		}
	} else {
		prefix := []byte(monitorID + "\x00")
		for day, db := range h.databases {
			if err := ctx.Err(); err != nil {
				return nil, "", err
			}
			if _, err := os.Stat(filepath.Join(h.dir, day+".db")); err != nil {
				return nil, "", ErrHistoryUnavailable
			}
			err := db.View(func(tx *bolt.Tx) error {
				bucket := tx.Bucket([]byte("events"))
				if bucket == nil {
					return ErrHistoryUnavailable
				}
				cur := bucket.Cursor()
				inspected, matched := 0, 0
				for k, raw := cur.Seek(append(bytes.Clone(prefix), []byte(after)...)); k != nil && bytes.HasPrefix(k, prefix); k, raw = cur.Next() {
					if err := ctx.Err(); err != nil {
						return err
					}
					position := string(k[len(prefix):])
					if position <= after {
						continue
					}
					if position > v.upper {
						break
					}
					if !validHistoryPosition(position) || len(raw) > 256<<10 {
						return ErrHistoryUnavailable
					}
					var e Event
					if json.Unmarshal(raw, &e) != nil || e.ID != position || e.MonitorID != monitorID {
						return ErrHistoryUnavailable
					}
					accept(e)
					inspected++
					if !e.At.Before(v.cutoff) {
						matched++
					}
					if matched > limit {
						break
					}
					if inspected == scanLimit {
						if position < frontier {
							frontier = position
						}
						break
					}
				}
				return nil
			})
			if err != nil {
				return nil, "", err
			}
		}
	}
	items := make([]Event, 0, candidates.Len())
	for _, e := range candidates {
		if e.ID <= frontier {
			items = append(items, e)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	if len(items) > limit {
		items = items[:limit]
		return items, items[len(items)-1].ID, nil
	}
	if frontier < v.upper {
		return items, frontier, nil
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
