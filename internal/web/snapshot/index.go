package snapshot

import (
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Index has an immutable name-ordered catalog and one atomic pointer per row.
// HTTP filtering takes no controller lock and allocates only the requested page.
// A page can observe concurrent row updates; catalog order and IDs remain stable.
type Index struct {
	rows      map[uint32]*atomic.Pointer[MonitorSummary]
	order     []uint32
	mu        sync.RWMutex
	aggregate StatsSnapshot
}

func NewIndex(monitors []MonitorSummary) *Index {
	slices.SortFunc(monitors, func(a, b MonitorSummary) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	i := &Index{rows: make(map[uint32]*atomic.Pointer[MonitorSummary], len(monitors)), order: make([]uint32, 0, len(monitors)), aggregate: StatsSnapshot{ByStatus: map[string]int{}, ByPulseType: map[string]int{}, ByCode: map[string]int{}}}
	for _, m := range monitors {
		i.rows[m.ID] = &atomic.Pointer[MonitorSummary]{}
		i.order = append(i.order, m.ID)
		i.Put(m)
	}
	return i
}

func (i *Index) Put(m MonitorSummary) {
	p, ok := i.rows[m.ID]
	if !ok {
		return
	}
	old := p.Swap(&m)
	i.mu.Lock()
	defer i.mu.Unlock()
	if old == nil {
		i.aggregate.Total++
	} else {
		i.count(*old, -1)
	}
	i.count(m, 1)
	i.aggregate.Generated = time.Now()
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
	p, ok := i.rows[id]
	if !ok {
		return MonitorSummary{}, false
	}
	m := p.Load()
	if m == nil {
		return MonitorSummary{}, false
	}
	return *m, true
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
	rows := make([]MonitorSummary, 0, limit)
	total := 0
	if match == nil {
		end := min(len(i.order), offset+limit)
		for n := max(0, offset); n < end; n++ {
			m, _ := i.Get(i.order[n])
			rows = append(rows, m)
		}
		return rows, len(i.order)
	}
	for _, id := range i.order {
		m, _ := i.Get(id)
		if !match(m) {
			continue
		}
		if total >= offset && len(rows) < limit {
			rows = append(rows, m)
		}
		total++
	}
	return rows, total
}
