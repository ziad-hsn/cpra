package durable

import (
	"cpra/internal/slo"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/hashicorp/raft"
)

type envelope struct {
	Version  int       `json:"version"`
	Commands []Command `json:"commands"`
}
type image struct {
	SLO      slo.State          `json:"slo"`
	Version  int                `json:"version"`
	Index    uint64             `json:"index"`
	Monitors map[string]Monitor `json:"monitors"`
}
type machine struct {
	mu      sync.RWMutex
	image   image
	history *HistoryStore
	err     error
}

func (f *machine) Apply(log *raft.Log) any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	var batch envelope
	if err := json.Unmarshal(log.Data, &batch); err != nil || batch.Version != FormatVersion {
		f.err = fmt.Errorf("incompatible or corrupt command at log index %d", log.Index)
		return f.err
	}
	for _, c := range batch.Commands {
		if err := validateCommand(c); err != nil {
			f.err = fmt.Errorf("invalid committed command at %d: %w", log.Index, err)
			return f.err
		}
	}
	results := make([]Result, 0, len(batch.Commands))
	var events []Event
	var at time.Time
	for _, c := range batch.Commands {
		if c.At.After(at) {
			at = c.At
		}
		if c.Kind == "slo" {
			f.image.SLO = c.SLO.Clone()
			results = append(results, Result{Allowed: true})
			continue
		}
		if c.Kind == "recover" {
			ids := make([]string, 0)
			for id, m := range f.image.Monitors {
				for _, a := range m.Actions {
					if a.State == Started {
						ids = append(ids, id)
						break
					}
				}
			}
			slices.Sort(ids)
			for _, id := range ids {
				r := transition(f.image.Monitors[id], c)
				f.image.Monitors[id] = *r.Monitor
				events = append(events, r.Events...)
			}
			results = append(results, Result{Allowed: true})
			continue
		}
		r := transition(f.image.Monitors[c.MonitorID], c)
		if r.Monitor != nil {
			f.image.Monitors[c.MonitorID] = *r.Monitor
			copy := r.Monitor.Clone()
			r.Monitor = &copy
		}
		events = append(events, r.Events...)
		results = append(results, r)
	}
	for n := range events {
		events[n].ID = fmt.Sprintf("%020d:%08d", log.Index, n)
	}
	if err := f.history.append(log.Index, events, at); err != nil {
		f.err = fmt.Errorf("durable history commit failed: %w", err)
		return f.err
	}
	f.image.Index = log.Index
	return results
}

func (f *machine) Snapshot() (raft.FSMSnapshot, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.err != nil {
		return nil, f.err
	}
	f.history.mu.RLock()
	index := f.history.catalog.Index
	f.history.mu.RUnlock()
	if index < f.image.Index {
		return nil, fmt.Errorf("history has not reached the snapshot position")
	}
	i := image{Version: FormatVersion, Index: f.image.Index, SLO: f.image.SLO.Clone(), Monitors: make(map[string]Monitor, len(f.image.Monitors))}
	for k, m := range f.image.Monitors {
		i.Monitors[k] = m.Clone()
	}
	return &frozenSnapshot{image: i}, nil
}

func decodeImage(reader io.Reader) (image, error) {
	var i image
	d := json.NewDecoder(reader)
	if err := d.Decode(&i); err != nil {
		return i, fmt.Errorf("corrupt snapshot: %w", err)
	}
	if i.Version != FormatVersion || i.Monitors == nil {
		return i, fmt.Errorf("incompatible snapshot format %d", i.Version)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return i, fmt.Errorf("unexpected trailing snapshot data")
	}
	if err := i.SLO.Validate(); err != nil {
		return i, err
	}
	for id, m := range i.Monitors {
		if id == "" || m.ID != id || m.Revision == "" || m.Policy.Interval <= 0 {
			return i, fmt.Errorf("invalid monitor in snapshot")
		}
	}
	return i, nil
}

func (f *machine) Restore(reader io.ReadCloser) error {
	defer reader.Close()
	i, err := decodeImage(reader)
	if err != nil {
		return err
	}
	f.history.mu.RLock()
	index := f.history.catalog.Index
	f.history.mu.RUnlock()
	if index < i.Index {
		return fmt.Errorf("history catalog is missing committed snapshot history; restore the complete backup")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.image = i
	return nil
}

type frozenSnapshot struct{ image image }

func (s *frozenSnapshot) Persist(sink raft.SnapshotSink) error {
	if err := json.NewEncoder(sink).Encode(s.image); err != nil {
		_ = sink.Cancel()
		return err
	}
	if err := sink.Close(); err != nil {
		_ = sink.Cancel()
		return err
	}
	return nil
}
func (s *frozenSnapshot) Release() { s.image.Monitors = nil }
