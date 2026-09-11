package durable

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

var ErrHistoryUnavailable = errors.New("history unavailable")

type historyCatalog struct {
	Version  int             `json:"version"`
	Index    uint64          `json:"index"`
	Segments map[string]bool `json:"segments"` // false marks a committed retirement
	Cutoff   time.Time       `json:"cutoff"`
}

type HistoryStore struct {
	mu        sync.RWMutex
	dir       string
	catalog   historyCatalog
	databases map[string]*bolt.DB
	memory    []Event
	err       error
}

type HistoryPage struct {
	Events        []Event `json:"events"`
	NextCursor    string  `json:"next_cursor,omitempty"`
	RetentionDays int     `json:"retention_days"`
}

type historyCursor struct{ MonitorID, After, Upper string }

func openHistory(dir string) (*HistoryStore, error) {
	h := &HistoryStore{dir: dir, catalog: historyCatalog{Version: FormatVersion, Segments: make(map[string]bool)}, databases: make(map[string]*bolt.DB)}
	if dir == "" {
		return h, nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(dir, "catalog.json"))
	if err == nil {
		if err = json.Unmarshal(b, &h.catalog); err != nil || h.catalog.Version != FormatVersion || h.catalog.Segments == nil {
			return nil, fmt.Errorf("%w: corrupt or incompatible history catalog", ErrHistoryUnavailable)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for day, active := range h.catalog.Segments {
		if _, err := time.Parse("2006-01-02", day); err != nil {
			h.Close()
			return nil, fmt.Errorf("%w: invalid segment name", ErrHistoryUnavailable)
		}
		path := filepath.Join(dir, day+".db")
		if !active {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				h.Close()
				return nil, err
			}
			continue
		}
		if _, err := os.Stat(path); err != nil {
			h.Close()
			return nil, fmt.Errorf("%w: segment %s missing", ErrHistoryUnavailable, day)
		}
		db, err := openSegment(path)
		if err != nil {
			h.Close()
			return nil, fmt.Errorf("%w: segment %s: %w", ErrHistoryUnavailable, day, err)
		}
		h.databases[day] = db
	}
	return h, nil
}

func openSegment(path string) (*bolt.DB, error) {
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, err
	}
	if err = db.View(func(tx *bolt.Tx) error {
		for err := range tx.Check() {
			return err
		}
		return nil
	}); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (h *HistoryStore) append(index uint64, events []Event, at time.Time) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.err != nil {
		return h.err
	}
	if index <= h.catalog.Index {
		return nil
	}
	if h.dir == "" {
		h.memory = append(h.memory, events...)
		h.catalog.Index = index
		h.catalog.Cutoff = at.AddDate(0, 0, -30)
		h.memory = slices.DeleteFunc(h.memory, func(e Event) bool { return e.At.Before(h.catalog.Cutoff) })
		return nil
	}
	groups := make(map[string][]Event)
	for _, e := range events {
		if e.At.Before(h.catalog.Cutoff) {
			continue
		}
		day := e.At.UTC().Format("2006-01-02")
		groups[day] = append(groups[day], e)
	}
	for day, group := range groups {
		db := h.databases[day]
		if db == nil {
			var err error
			db, err = openSegment(filepath.Join(h.dir, day+".db"))
			if err != nil {
				h.err = err
				return err
			}
			h.databases[day] = db
		}
		err := db.Update(func(tx *bolt.Tx) error {
			b, err := tx.CreateBucketIfNotExists([]byte("events"))
			if err != nil {
				return err
			}
			for _, e := range group {
				data, err := json.Marshal(e)
				if err != nil {
					return err
				}
				if err = b.Put([]byte(e.MonitorID+"\x00"+e.ID), data); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			h.err = err
			return err
		}
		h.catalog.Segments[day] = true
	}
	h.catalog.Index = index
	// The segment writes precede this synced watermark. Replay repairs any
	// interruption between them using the same (log position, ordinal) keys.
	h.err = atomicJSON(filepath.Join(h.dir, "catalog.json"), h.catalog)
	return h.err
}

func (h *HistoryStore) Expire(at time.Time) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	cutoff := at.UTC().AddDate(0, 0, -30)
	if !cutoff.After(h.catalog.Cutoff) {
		return nil
	}
	h.catalog.Cutoff = cutoff
	for day := range h.catalog.Segments {
		d, err := time.Parse("2006-01-02", day)
		if err != nil {
			return err
		}
		if !d.AddDate(0, 0, 1).After(cutoff) {
			h.catalog.Segments[day] = false
		}
	}
	if h.dir == "" {
		h.memory = slices.DeleteFunc(h.memory, func(e Event) bool { return e.At.Before(cutoff) })
		return nil
	}
	if err := atomicJSON(filepath.Join(h.dir, "catalog.json"), h.catalog); err != nil {
		h.err = err
		return err
	}
	for day, active := range h.catalog.Segments {
		if active {
			continue
		}
		if db := h.databases[day]; db != nil {
			if err := db.Close(); err != nil {
				h.err = err
				return err
			}
			delete(h.databases, day)
		}
		if err := os.Remove(filepath.Join(h.dir, day+".db")); err != nil && !errors.Is(err, os.ErrNotExist) {
			h.err = err
			return err
		}
		delete(h.catalog.Segments, day)
	}
	h.err = atomicJSON(filepath.Join(h.dir, "catalog.json"), h.catalog)
	return h.err
}

func (h *HistoryStore) Page(monitorID, cursor string, limit int) (HistoryPage, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	p := HistoryPage{Events: []Event{}, RetentionDays: 30}
	if h.err != nil {
		return p, fmt.Errorf("%w: %w", ErrHistoryUnavailable, h.err)
	}
	if monitorID == "" || strings.ContainsRune(monitorID, 0) {
		return p, fmt.Errorf("monitor_id is required")
	}
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 500 {
		return p, fmt.Errorf("history limit must be between 1 and 500")
	}
	c := historyCursor{MonitorID: monitorID, Upper: fmt.Sprintf("%020d:%08d", h.catalog.Index, 99999999)}
	if cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || len(data) > 2048 || json.Unmarshal(data, &c) != nil || c.MonitorID != monitorID {
			return p, fmt.Errorf("invalid history cursor")
		}
	}
	accept := func(e Event) {
		if e.MonitorID == monitorID && e.ID > c.After && e.ID <= c.Upper && !e.At.Before(h.catalog.Cutoff) {
			p.Events = append(p.Events, e)
		}
	}
	if h.dir == "" {
		for _, e := range h.memory {
			accept(e)
		}
	} else {
		prefix := []byte(monitorID + "\x00")
		for day, db := range h.databases {
			if _, err := os.Stat(filepath.Join(h.dir, day+".db")); err != nil {
				return p, fmt.Errorf("%w: segment %s", ErrHistoryUnavailable, day)
			}
			err := db.View(func(tx *bolt.Tx) error {
				b := tx.Bucket([]byte("events"))
				if b == nil {
					return nil
				}
				cur := b.Cursor()
				count := 0
				for k, v := cur.Seek(append(bytes.Clone(prefix), []byte(c.After)...)); k != nil && bytes.HasPrefix(k, prefix); k, v = cur.Next() {
					var e Event
					if err := json.Unmarshal(v, &e); err != nil {
						return err
					}
					before := len(p.Events)
					accept(e)
					if len(p.Events) > before {
						count++
					}
					if count > limit {
						break
					}
				}
				return nil
			})
			if err != nil {
				return p, fmt.Errorf("%w: %w", ErrHistoryUnavailable, err)
			}
		}
	}
	slices.SortFunc(p.Events, func(a, b Event) int { return strings.Compare(a.ID, b.ID) })
	if len(p.Events) > limit {
		p.Events = p.Events[:limit]
		c.After = p.Events[len(p.Events)-1].ID
		data, _ := json.Marshal(c)
		p.NextCursor = base64.RawURLEncoding.EncodeToString(data)
	}
	return p, nil
}

func (h *HistoryStore) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	var err error
	for _, db := range h.databases {
		err = errors.Join(err, db.Close())
	}
	h.databases = make(map[string]*bolt.DB)
	return err
}
