package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/btree"
	bolt "go.etcd.io/bbolt"
)

var (
	ErrOperationSnapshotQuota = errors.New("operation snapshot byte quota exceeded")
	ErrOperationCursorExpired = errors.New("operation view expired; start a new operation list")
)

const (
	maxOperationSegments      = 64
	maxOperationInspectedKeys = 10000
)

// OperationView freezes only the bounded live set. Terminal indexes remain on
// disk; immutable terminal positions are limited by this view's log watermark.
type OperationView struct {
	store                                    *Store
	live                                     []OperationReceipt
	liveIDs                                  map[string]struct{}
	epoch                                    string
	highWater                                uint64
	upper                                    string
	cutoff                                   time.Time
	retentionGeneration, operationGeneration uint64
	segments                                 []string
	bytes                                    int64
}

func (v OperationView) EstimatedBytes() int64 { return v.bytes }
func operationReceiptBytes(r OperationReceipt) int64 {
	return int64(768 + len(r.ID)*2 + len(r.Key.Kind) + len(r.Key.ID) + len(r.UID) + len(r.OldVersion) + len(r.NewVersion) + len(r.Actor) + len(r.Subject) + len(r.ActionID) + len(r.IncidentID) + len(r.InvalidatedByRestore))
}

// OperationSnapshot checks maxBytes before copying live state. The HTTP owner
// supplies its remaining global/principal budget, and charges EstimatedBytes.
func (s *Store) OperationSnapshot(at time.Time, maxBytes int64) (OperationView, error) {
	if maxBytes < 512 {
		return OperationView{}, ErrOperationSnapshotQuota
	}
	if at.IsZero() {
		return OperationView{}, ErrOperationReservation
	}
	if !s.Status().Ready {
		return OperationView{}, ErrHistoryUnavailable
	}
	s.fsm.mu.RLock()
	defer s.fsm.mu.RUnlock()
	h := s.fsm.history
	h.mu.RLock()
	defer h.mu.RUnlock()
	return s.operationSnapshotLocked(context.Background(), at, maxBytes)
}

// operationSnapshotLocked captures ordinary receipts while the caller holds the
// FSM and history read locks. A management snapshot adds collection metadata at
// this same boundary; no independently timed snapshot is composed afterward.
func (s *Store) operationSnapshotLocked(ctx context.Context, at time.Time, maxBytes int64) (OperationView, error) {
	h := s.fsm.history
	if h.closed || h.err != nil || h.catalog.Index < s.fsm.image.Index {
		return OperationView{}, ErrHistoryUnavailable
	}
	required := int64(512)
	count := 0
	for _, active := range h.catalog.Segments {
		if active {
			count++
		}
	}
	if count > maxOperationSegments {
		return OperationView{}, ErrHistoryUnavailable
	}
	required += int64(count * 64)
	for _, r := range s.fsm.image.Operations {
		if err := ctx.Err(); err != nil {
			return OperationView{}, err
		}
		required += operationReceiptBytes(r)
	}
	for _, r := range s.fsm.image.OperationReservations {
		if err := ctx.Err(); err != nil {
			return OperationView{}, err
		}
		required += operationReceiptBytes(r.OperationReceipt)
	}
	if required > maxBytes {
		return OperationView{}, ErrOperationSnapshotQuota
	}
	v := OperationView{store: s, epoch: s.fsm.image.OperationEpoch, highWater: s.fsm.image.OperationHighWater, upper: fmt.Sprintf("%020d:%08d", s.fsm.image.Index, 99999999), cutoff: h.catalog.Cutoff, retentionGeneration: h.retentionGeneration, operationGeneration: h.operationGeneration, bytes: required,
		live: make([]OperationReceipt, 0, s.fsm.pendingOperationCount()), liveIDs: make(map[string]struct{}, s.fsm.pendingOperationCount()), segments: make([]string, 0, count)}
	for _, r := range s.fsm.image.Operations {
		v.live = append(v.live, r)
		v.liveIDs[r.ID] = struct{}{}
	}
	for _, r := range s.fsm.image.OperationReservations {
		v.liveIDs[r.ID] = struct{}{}
		if at.Before(r.ExpiresAt) {
			v.live = append(v.live, r.OperationReceipt)
		}
	}
	for day, active := range h.catalog.Segments {
		if active {
			if h.databases[day] == nil {
				return OperationView{}, ErrHistoryUnavailable
			}
			v.segments = append(v.segments, day)
		}
	}
	slices.Sort(v.segments)
	slices.SortFunc(v.live, func(a, b OperationReceipt) int { return strings.Compare(a.ID, b.ID) })
	return v, nil
}
func (v OperationView) valid() error {
	if v.store == nil {
		return ErrOperationCursorExpired
	}
	select {
	case <-v.store.stop:
		return ErrOperationCursorExpired
	default:
	}
	v.store.fsm.mu.RLock()
	epoch := v.store.fsm.image.OperationEpoch
	v.store.fsm.mu.RUnlock()
	if epoch != v.epoch {
		return ErrOperationCursorExpired
	}
	if !v.store.Status().Ready {
		return ErrHistoryUnavailable
	}
	return nil
}

// Page returns live handles first, then terminal event positions. after is an
// internal phase/position token, wrapped by the authenticated HTTP cursor.
func (v OperationView) Page(ctx context.Context, monitorID, after string, limit int) ([]OperationReceipt, string, error) {
	if ctx == nil || limit < 1 || limit > 500 || monitorID != "" && (!catalogIdentifier(monitorID, 256) || strings.ContainsRune(monitorID, 0)) {
		return nil, "", ErrOperationReservation
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if err := v.valid(); err != nil {
		return nil, "", err
	}
	phase, position := "l", ""
	if after != "" {
		if len(after) > 258 || len(after) < 2 || after[1] != ':' {
			return nil, "", ErrOperationReservation
		}
		phase, position = after[:1], after[2:]
		if phase != "l" && phase != "h" || phase == "h" && position != "" && !validHistoryPosition(position) || phase == "l" && !catalogIdentifier(position, 256) {
			return nil, "", ErrOperationReservation
		}
	}
	h := v.store.fsm.history
	h.mu.RLock()
	defer h.mu.RUnlock()
	return v.pageLocked(ctx, h, monitorID, phase, position, limit)
}

// pageLocked is shared by the ordinary and management snapshot paths. It needs
// the history lock, but no FSM or Store lock, while reading indexed history.
func (v OperationView) pageLocked(ctx context.Context, h *HistoryStore, monitorID, phase, position string, limit int) ([]OperationReceipt, string, error) {
	return v.pageLockedBudget(ctx, h, monitorID, phase, position, limit, nil)
}

// operationReadBudget counts source/index work across all families in a unified
// page. A nil budget preserves the legacy live-phase behavior.
type operationReadBudget struct{ remaining int }

func (b *operationReadBudget) take(n int) bool {
	if b == nil {
		return true
	}
	if n > b.remaining {
		return false
	}
	b.remaining -= n
	return true
}

func (v OperationView) pageLockedBudget(ctx context.Context, h *HistoryStore, monitorID, phase, position string, limit int, budget *operationReadBudget) ([]OperationReceipt, string, error) {
	if h.closed {
		return nil, "", ErrOperationCursorExpired
	}
	if h.err != nil {
		return nil, "", ErrHistoryUnavailable
	}
	if h.retentionGeneration != v.retentionGeneration || h.operationGeneration != v.operationGeneration {
		return nil, "", ErrOperationCursorExpired
	}
	if !budget.take(8 * len(v.segments)) {
		return nil, phase + ":" + position, nil
	}
	// Check all captured segment identities/index readiness even if live rows fill
	// this page; a missing segment must not masquerade as a complete list.
	for _, day := range v.segments {
		db := h.databases[day]
		if db == nil {
			return nil, "", ErrHistoryUnavailable
		}
		if _, err := os.Stat(filepath.Join(h.dir, day+".db")); err != nil {
			return nil, "", ErrHistoryUnavailable
		}
		if err := db.View(func(tx *bolt.Tx) error {
			ready, err := operationIndexReady(tx)
			if err != nil || !ready {
				return ErrHistoryUnavailable
			}
			return nil
		}); err != nil {
			return nil, "", ErrHistoryUnavailable
		}
	}
	items := make([]OperationReceipt, 0, limit)
	if phase == "l" {
		frontier := position
		for _, r := range v.live {
			if err := ctx.Err(); err != nil {
				return nil, "", err
			}
			if r.ID <= position {
				continue
			}
			if !budget.take(1) {
				return items, "l:" + frontier, nil
			}
			frontier = r.ID
			if monitorID != "" && (r.Key.Kind != "Monitor" || r.Key.ID != monitorID) {
				continue
			}
			items = append(items, r)
			if len(items) == limit {
				return items, "l:" + r.ID, nil
			}
		}
		position = ""
	}
	terminal, next, err := v.terminalPageBudget(ctx, h, monitorID, position, limit-len(items), budget)
	if err != nil {
		return nil, "", err
	}
	items = append(items, terminal...)
	return items, next, nil
}

type operationPageSource struct {
	tx      *bolt.Tx
	cur     *bolt.Cursor
	tree    *btree.BTreeG[operationMemoryItem]
	prefix  string
	key     string
	primary []byte
	memory  Event
	started bool
	ready   bool
}

func (s *operationPageSource) advance(after, upper string) error {
	if s.tree != nil {
		pivot := s.prefix + after
		s.ready = false
		s.tree.AscendGreaterOrEqual(operationMemoryItem{key: pivot}, func(item operationMemoryItem) bool {
			if item.key <= pivot {
				return true
			}
			if !strings.HasPrefix(item.key, s.prefix) {
				return false
			}
			position := strings.TrimPrefix(item.key, s.prefix)
			if position > upper {
				return false
			}
			s.key, s.memory, s.ready = position, item.event, true
			return false
		})
		return nil
	}
	var k, val []byte
	if !s.started {
		k, val = s.cur.Seek([]byte(s.prefix + after))
		if bytes.Equal(k, []byte(s.prefix+after)) {
			k, val = s.cur.Next()
		}
		s.started = true
	} else {
		k, val = s.cur.Next()
	}
	s.ready = k != nil && bytes.HasPrefix(k, []byte(s.prefix))
	if !s.ready {
		return nil
	}
	s.key = string(k[len(s.prefix):])
	if !validHistoryPosition(s.key) || len(val) > 1024 {
		return ErrHistoryUnavailable
	}
	if s.key > upper {
		s.ready = false
		return nil
	}
	s.primary = val
	return nil
}
func (s *operationPageSource) event() (Event, error) {
	if s.tree != nil {
		return s.memory, nil
	}
	primary := s.tx.Bucket([]byte("events"))
	if primary == nil {
		return Event{}, ErrHistoryUnavailable
	}
	raw := primary.Get(s.primary)
	if len(raw) > maxOperationEventBytes {
		return Event{}, ErrHistoryUnavailable
	}
	var e Event
	if json.Unmarshal(raw, &e) != nil || string(s.primary) != e.MonitorID+"\x00"+e.ID {
		return Event{}, ErrHistoryUnavailable
	}
	return e, nil
}
func (v OperationView) terminalPageBudget(ctx context.Context, h *HistoryStore, monitorID, after string, limit int, budget *operationReadBudget) ([]OperationReceipt, string, error) {
	if budget == nil {
		budget = &operationReadBudget{remaining: maxOperationInspectedKeys}
	}
	sourceCount := len(v.segments)
	if h.dir == "" {
		sourceCount = 1
	}
	if !budget.take(8 * sourceCount) {
		return nil, "h:" + after, nil
	}
	sources := make([]*operationPageSource, 0, len(v.segments)+1)
	defer func() {
		for _, source := range sources {
			if source.tx != nil {
				_ = source.tx.Rollback()
			}
		}
	}()
	prefix := ""
	if monitorID != "" {
		prefix = monitorID + "\x00"
	}
	if h.dir == "" {
		tree := h.memoryOperationTree
		if monitorID != "" {
			tree = h.memoryMonitorOperations
		}
		if tree != nil {
			sources = append(sources, &operationPageSource{tree: tree, prefix: prefix})
		}
	} else {
		for _, day := range v.segments {
			tx, err := h.databases[day].Begin(false)
			if err != nil {
				return nil, "", ErrHistoryUnavailable
			}
			source := &operationPageSource{tx: tx, prefix: prefix}
			sources = append(sources, source)
			name := operationTerminalBucket
			if monitorID != "" {
				name = operationMonitorBucket
			}
			bucket := tx.Bucket(name)
			if bucket == nil {
				return nil, "", ErrHistoryUnavailable
			}
			source.cur = bucket.Cursor()
		}
	}
	for _, source := range sources {
		if err := source.advance(after, v.upper); err != nil {
			return nil, "", err
		}
	}
	items := make([]OperationReceipt, 0, limit+1)
	frontier := after
	lastAccepted := ""
	// Each candidate reserves receipt reconciliation as well as the primary
	// lookup, even if ownership/watermark filters later skip that candidate.
	for {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		selected := -1
		for i, source := range sources {
			if source.ready && (selected < 0 || source.key < sources[selected].key) {
				selected = i
			}
		}
		if selected < 0 {
			return items, "", nil
		}
		source := sources[selected]
		if !budget.take(2 + 2*len(sources)) {
			return items, "h:" + frontier, nil
		}
		if source.key <= frontier {
			return nil, "", ErrHistoryUnavailable
		}
		e, err := source.event()
		if err != nil {
			return nil, "", err
		}
		if validateOperationEvent(e) != nil || !terminalOperation(*e.Operation) || e.ID != source.key || monitorID != "" && (e.Operation.Key.Kind != "Monitor" || e.Operation.Key.ID != monitorID) {
			return nil, "", ErrHistoryUnavailable
		}
		accept := !e.At.Before(v.cutoff)
		if _, live := v.liveIDs[e.Operation.ID]; live {
			accept = false
		}
		if e.Operation.Outcome == "reservation_expired" {
			accept = false
		}
		if !isLegacyOperation(e.Operation.ID) {
			epoch, seq, err := ParseOperationHandle(e.Operation.ID)
			if err != nil {
				return nil, "", ErrHistoryUnavailable
			}
			accept = accept && epoch == v.epoch && seq <= v.highWater
		}
		if accept {
			latest, err := latestLegacyOperation(h, sources, e.Operation.ID, v.upper, v.cutoff)
			if err != nil {
				return nil, "", err
			}
			if !isLegacyOperation(e.Operation.ID) && latest.ID != e.ID {
				return nil, "", ErrHistoryUnavailable
			}
			accept = latest.ID == e.ID
		}
		frontier = e.ID
		if accept {
			items = append(items, *e.Operation)
		}
		if len(items) > limit {
			return items[:limit], "h:" + lastAccepted, nil
		}
		if accept {
			lastAccepted = e.ID
		}
		if err := source.advance(frontier, v.upper); err != nil {
			return nil, "", err
		}
	}
}

// latestLegacyOperation selects the authoritative retained receipt. Legacy IDs
// have no highwater; a generation fence invalidates views on any later legacy
// write, so the selected terminal cannot change behind an existing cursor.
func latestLegacyOperation(h *HistoryStore, sources []*operationPageSource, id, upper string, cutoff time.Time) (Event, error) {
	var latest Event
	if h.dir == "" {
		e, ok := h.memoryOperations[id]
		if !ok {
			return latest, ErrHistoryUnavailable
		}
		return e, nil
	}
	for _, source := range sources {
		b := source.tx.Bucket([]byte("operations"))
		if b == nil {
			continue
		}
		raw := b.Get([]byte(id))
		if raw == nil {
			continue
		}
		if len(raw) > maxOperationEventBytes {
			return latest, ErrHistoryUnavailable
		}
		var e Event
		if json.Unmarshal(raw, &e) != nil || validateOperationEvent(e) != nil || e.Operation.ID != id {
			return latest, ErrHistoryUnavailable
		}
		primary := source.tx.Bucket([]byte("events"))
		if primary == nil || !bytes.Equal(primary.Get([]byte(e.MonitorID+"\x00"+e.ID)), raw) {
			return latest, ErrHistoryUnavailable
		}
		if e.ID <= upper && !e.At.Before(cutoff) && e.ID > latest.ID {
			latest = e
		}
	}
	if latest.Operation == nil {
		return latest, ErrHistoryUnavailable
	}
	return latest, nil
}
