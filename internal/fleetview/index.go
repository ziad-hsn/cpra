package fleetview

import (
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/btree"
)

// Index supports dynamic membership with one atomic observation pointer per row.
// Membership/rename edits update a name-ordered tree in O(log N); ordinary check
// results do not rebuild that tree. Pages clone its membership in O(1) and filter
// outside the writer lock. V1 observations may change while a page is read;
// management resource cursors use their separate immutable durable catalog.
type Index struct {
	rows      map[uint32]*atomic.Pointer[MonitorSummary]
	order     *btree.BTreeG[indexItem]
	mu        sync.RWMutex
	aggregate StatsSnapshot
}

type indexItem struct {
	name string
	id   uint32
	row  *atomic.Pointer[MonitorSummary]
}

func NewIndex(monitors []MonitorSummary) *Index {
	order := btree.NewG[indexItem](32, func(a, b indexItem) bool {
		if c := strings.Compare(a.name, b.name); c != 0 {
			return c < 0
		}
		return a.id < b.id
	})
	i := &Index{rows: make(map[uint32]*atomic.Pointer[MonitorSummary], len(monitors)), order: order, aggregate: StatsSnapshot{ByStatus: map[string]int{}, ByPulseType: map[string]int{}, ByCode: map[string]int{}}}
	for _, m := range monitors {
		i.Put(m)
	}
	return i
}

func (i *Index) Put(m MonitorSummary) {
	m.ActiveCodes = slices.Clone(m.ActiveCodes)
	i.mu.Lock()
	defer i.mu.Unlock()
	p, ok := i.rows[m.ID]
	if !ok {
		p = &atomic.Pointer[MonitorSummary]{}
		i.rows[m.ID] = p
	}
	old := p.Swap(&m)
	if old == nil {
		i.aggregate.Total++
	} else {
		i.count(*old, -1)
	}
	if old == nil || old.Name != m.Name {
		if old != nil {
			i.order.Delete(indexItem{name: old.Name, id: old.ID})
		}
		i.order.ReplaceOrInsert(indexItem{name: m.Name, id: m.ID, row: p})
	}
	i.count(m, 1)
	i.aggregate.Generated = time.Now()
}

// Remove removes one live observation row without mutating any retained reader
// view. The controller must fence old executions before reusing an entity ID.
func (i *Index) Remove(id uint32) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	p, ok := i.rows[id]
	if !ok {
		return false
	}
	m := p.Load()
	delete(i.rows, id)
	i.order.Delete(indexItem{name: m.Name, id: id})
	i.count(*m, -1)
	i.aggregate.Total--
	i.aggregate.Generated = time.Now()
	return true
}
func (i *Index) count(m MonitorSummary, delta int) {
	i.aggregate.ByStatus[m.Status] += delta
	i.aggregate.ByPulseType[m.PulseType] += delta
	if m.PendingCode != "" {
		i.aggregate.ByCode[m.PendingCode] += delta
	}
	if m.Status == "disabled" {
		i.aggregate.Disabled += delta
	}
}
func (i *Index) Touch(at time.Time) { i.mu.Lock(); i.aggregate.Generated = at; i.mu.Unlock() }
func (i *Index) Get(id uint32) (MonitorSummary, bool) {
	i.mu.RLock()
	p, ok := i.rows[id]
	i.mu.RUnlock()
	if !ok {
		return MonitorSummary{}, false
	}
	m := p.Load()
	if m == nil {
		return MonitorSummary{}, false
	}
	copy := *m
	copy.ActiveCodes = slices.Clone(copy.ActiveCodes)
	return copy, true
}
func (i *Index) Overview() *StatsSnapshot {
	i.mu.RLock()
	defer i.mu.RUnlock()
	s := i.aggregate
	s.ByStatus = maps.Clone(s.ByStatus)
	s.ByPulseType = maps.Clone(s.ByPulseType)
	s.ByCode = maps.Clone(s.ByCode)
	return &s
}
func (i *Index) Page(offset, limit int, match func(MonitorSummary) bool) ([]MonitorSummary, int) {
	offset = max(0, offset)
	limit = min(500, max(0, limit))
	// Clone mutates the B-tree copy-on-write context, so an exclusive lock is
	// required even though it captures a read view. No filtering holds this lock.
	i.mu.Lock()
	view := i.order.Clone()
	i.mu.Unlock()
	rows := make([]MonitorSummary, 0, limit)
	total := 0
	view.Ascend(func(item indexItem) bool {
		m := *item.row.Load()
		m.ActiveCodes = slices.Clone(m.ActiveCodes)
		if match != nil && !match(m) {
			return true
		}
		if total >= offset && len(rows) < limit {
			rows = append(rows, m)
		}
		total++
		return match != nil || len(rows) < limit
	})
	if match == nil {
		total = view.Len()
	}
	return rows, total
}
