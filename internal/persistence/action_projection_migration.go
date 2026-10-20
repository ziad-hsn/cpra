package persistence

import (
	"context"
	"errors"
	"github.com/google/btree"
	"reflect"
	"slices"
	"strings"
	"time"
)

var errActionProjectionPending = errors.New("action storage migration is incomplete")

// The marker is committed after replay. Old 44/45 envelopes retain synchronous
// transition semantics until this cutover, including already logged blue starts.
type actionProjectionState struct {
	Version  int                        `json:"version"`
	Ready    bool                       `json:"ready"`
	Revision uint64                     `json:"revision"`
	Current  *actionProjectionMigration `json:"current,omitempty"`
}
type actionProjectionMigration struct {
	MonitorID   string            `json:"monitor_id"`
	Fence       uint64            `json:"fence"`
	After       string            `json:"after,omitempty"`
	ReviewAfter string            `json:"review_after,omitempty"`
	Phase       string            `json:"phase"`
	Summary     *ActionProjection `json:"summary"`
}

func (s *actionProjectionState) clone() *actionProjectionState {
	if s == nil {
		return nil
	}
	v := *s
	if s.Current != nil {
		c := *s.Current
		c.Summary = c.Summary.clone()
		v.Current = &c
	}
	return &v
}
func (f *machine) projectionsEnabled() bool {
	return f.image.ActionProjectionState != nil && f.image.ActionProjectionState.Ready
}
func validateActionProjectionCommand(c Command) error {
	extra := c
	extra.Kind, extra.At, extra.MonitorID, extra.Revision, extra.Generation = "", time.Time{}, "", "", 0
	if extra != (Command{}) || c.At.Year() < 1 || c.At.Year() > 9999 {
		return errActionLedger
	}
	switch c.Revision {
	case "begin":
		if c.MonitorID != "" || c.Generation != 0 {
			return errActionLedger
		}
	case "finish":
		if c.MonitorID != "" || c.Generation == 0 {
			return errActionLedger
		}
	case "step":
		if !catalogIdentifier(c.MonitorID, 256) || c.Generation == 0 {
			return errActionLedger
		}
	default:
		return errActionLedger
	}
	return nil
}
func (f *machine) applyActionProjection(c Command) Result {
	state := f.image.ActionProjectionState
	if c.Revision == "begin" {
		if state == nil {
			f.image.ActionProjectionState = &actionProjectionState{Version: 1, Revision: 1}
		}
		return Result{Allowed: true}
	}
	if state == nil || state.Ready || state.Revision != c.Generation {
		return Result{Err: errActionProjectionPending}
	}
	next := state.clone()
	if c.Revision == "finish" {
		if next.Current != nil {
			return Result{Err: errActionProjectionPending}
		}
		for _, m := range f.image.Monitors {
			if actionStorageFormat(m.ActionVersion) && m.ActionProjection == nil {
				return Result{Err: errActionProjectionPending}
			}
		}
		next.Ready = true
		next.Revision++
		f.image.ActionProjectionState = next
		return Result{Allowed: true}
	}
	m, ok := f.image.Monitors[c.MonitorID]
	if !ok || !actionStorageFormat(m.ActionVersion) || m.ActionProjection != nil {
		return Result{Err: errActionProjectionPending}
	}
	if next.Current == nil {
		next.Current = &actionProjectionMigration{MonitorID: m.ID, Fence: f.image.ActionStorage.Sequence, Phase: "actions", Summary: &ActionProjection{Version: actionProjectionVersion}}
	}
	current := next.Current
	if current.MonitorID != m.ID {
		return Result{Err: errActionProjectionPending}
	}
	if current.Phase == "actions" {
		if current.Fence != 0 {
			pin, err := f.collections.pinActions()
			if err != nil {
				f.err = err
				return Result{Err: err}
			}
			pin.sequence = current.Fence
			rows, after, err := pin.pageBounded(context.Background(), m.ID, current.After, 64, 64, 1<<20)
			pin.Close()
			if err != nil {
				f.err = err
				return Result{Err: err}
			}
			for _, row := range rows {
				current.Summary.include(m, row.Action)
				if row.Action.ReviewNotice != nil && (row.Action.State == Queued || row.Action.State == Started) {
					current.Summary.PreviousReviewCapacity++
				}
			}
			current.After = after
			if after == "" {
				current.Phase = "reviews"
			}
		} else {
			current.Phase = "reviews"
		}
	} else {
		// The ordered index is rebuilt during cold recovery. Apply visits only
		// this page; it never sorts or copies the retained condition map.
		keys := make([]string, 0, 65)
		if tree := f.projectionReviews[m.ID]; tree != nil {
			tree.AscendGreaterOrEqual(current.ReviewAfter, func(key string) bool {
				if key > current.ReviewAfter {
					keys = append(keys, key)
				}
				return len(keys) < 65
			})
		}
		used, count := 0, 0
		for _, key := range keys {
			if count == 64 {
				break
			}
			alert := m.Notifications.ReviewAlerts[key]
			// Preserve the original owner across later file/catalog adoption.
			if alert.Owner == nil {
				a, found, err := f.actionForMonitor(m, alert.ActionID)
				if err != nil || !found {
					f.err = errActionLedger
					return Result{Err: f.err}
				}
				alert.Owner = &reviewAlertOwner{CatalogUID: a.CatalogUID, FileIncarnation: a.FileIncarnation}
			}
			row := reviewRecord{Key: reviewAlertKey(m.ID, key), MonitorID: m.ID, Sequence: f.image.ActionStorage.Sequence + 1, Alert: &alert}
			data, err := encodeReviewRecord(row)
			if err != nil {
				f.err = err
				return Result{Err: err}
			}
			if count > 0 && used+len(data) > 1<<20 {
				break
			}
			f.writeReview(row)
			current.Summary.ReviewConditions++
			current.ReviewAfter = key
			used += len(data)
			count++
		}
		if count == len(keys) {
			// Clone only active execution references. The retained maps remain
			// immutable until this image replaces them; snapshots own copies.
			m.Actions = make(map[string]Action)
			prefix := m.ID + "\x00"
			if f.actionsByMonitor != nil {
				f.actionsByMonitor.AscendGreaterOrEqual(actionItem{key: prefix}, func(item actionItem) bool {
					if !strings.HasPrefix(item.key, prefix) {
						return false
					}
					if item.action.State == Queued || item.action.State == Started {
						m.Actions[item.action.ID] = item.action.Clone()
					}
					return true
				})
			}
			if m.Notifications != nil {
				n := *m.Notifications
				n.ReviewAlerts = nil
				m.Notifications = n.clone()
			}
			m.ActionProjection = current.Summary
			if m.ActionProjection.PreviousReviewCapacity <= activeReviewDeliveries {
				m.ActionProjection.PreviousReviewCapacity = 0
			}
			m.unknownActions, m.heldActions = current.Summary.Unknown, current.Summary.Held
			f.image.Monitors[m.ID] = m
			delete(f.projectionReviews, m.ID)
			next.Current = nil
		}
	}
	next.Revision++
	f.image.ActionProjectionState = next
	return Result{Allowed: true}
}
func (s *Store) migrateActionProjection(ctx context.Context) error {
	submit := func(c Command) error {
		c.Kind = "action_project"
		c.At = time.Now().UTC()
		rows, err := s.Submit(ctx, []Command{c})
		if err != nil {
			return err
		}
		if len(rows) != 1 {
			return errActionLedger
		}
		if rows[0].Err != nil {
			return rows[0].Err
		}
		if !rows[0].Allowed {
			return errActionLedger
		}
		return nil
	}
	if err := submit(Command{Revision: "begin"}); err != nil {
		return err
	}
	// Store opening owns the sole mutation caller. Sorted IDs are a temporary
	// startup inventory, not retained action history or per-check work.
	s.fsm.mu.RLock()
	ids := make([]string, 0)
	for id, m := range s.fsm.image.Monitors {
		if actionStorageFormat(m.ActionVersion) && m.ActionProjection == nil {
			ids = append(ids, id)
		}
	}
	state := s.fsm.image.ActionProjectionState.clone()
	s.fsm.mu.RUnlock()
	if state.Ready {
		return nil
	}
	slices.Sort(ids)
	if state.Current != nil {
		for n, id := range ids {
			if id == state.Current.MonitorID {
				ids[0], ids[n] = ids[n], ids[0]
				break
			}
		}
	}
	for _, id := range ids {
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			s.fsm.mu.RLock()
			state = s.fsm.image.ActionProjectionState.clone()
			complete := s.fsm.image.Monitors[id].ActionProjection != nil
			s.fsm.mu.RUnlock()
			if complete {
				break
			}
			if err := submit(Command{Revision: "step", MonitorID: id, Generation: state.Revision}); err != nil {
				return err
			}
		}
	}
	s.fsm.mu.RLock()
	revision := s.fsm.image.ActionProjectionState.Revision
	s.fsm.mu.RUnlock()
	return submit(Command{Revision: "finish", Generation: revision})
}
func validateActionProjectionState(i image) error {
	s := i.ActionProjectionState
	for _, m := range i.Monitors {
		if m.ActionProjection != nil && s == nil || s != nil && s.Ready && actionStorageFormat(m.ActionVersion) && m.ActionProjection == nil {
			return errActionLedger
		}
	}
	if s == nil {
		return nil
	}
	if !actionStorageFormat(i.Version) || s.Version != 1 || s.Revision == 0 || s.Ready && s.Current != nil {
		return errActionLedger
	}
	if c := s.Current; c != nil {
		m, ok := i.Monitors[c.MonitorID]
		if !ok || m.ActionProjection != nil || i.ActionStorage == nil || c.Fence > i.ActionStorage.Sequence || c.Summary == nil || c.Summary.Version != 1 || c.Phase == "actions" && c.ReviewAfter != "" || c.Phase == "reviews" && c.After != "" || c.Phase != "actions" && c.Phase != "reviews" || c.After != "" && !catalogIdentifier(c.After, 256) || c.ReviewAfter != "" && !catalogIdentifier(c.ReviewAfter, 256) {
			return errActionLedger
		}
	}
	return nil
}
func validateActionProjectionMigration(i image, v *collectionLedgerView) error {
	s := i.ActionProjectionState
	if s == nil || s.Current == nil {
		return nil
	}
	c := s.Current
	m := i.Monitors[c.MonitorID]
	expected := &ActionProjection{Version: 1}
	if err := v.walkActions(func(_ []byte, row actionLedgerRow) error {
		if row.MonitorID == m.ID && (c.Phase == "reviews" || row.Action.ID <= c.After) {
			version, found, err := readActionVersion(v.tx, v.actions, row.Action.ID, c.Fence)
			if err != nil {
				return err
			}
			if !found {
				return nil
			}
			expected.include(m, version.Action)
			if version.Action.ReviewNotice != nil && (version.Action.State == Queued || version.Action.State == Started) {
				expected.PreviousReviewCapacity++
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if m.Notifications != nil {
		for id, original := range m.Notifications.ReviewAlerts {
			if id <= c.ReviewAfter {
				row, found, err := v.readReviewHead(reviewAlertKey(m.ID, id))
				if err != nil || !found || row.Alert == nil {
					return errActionLedger
				}
				if original.Owner == nil {
					action, found, err := readActionVersion(v.tx, v.actions, original.ActionID, c.Fence)
					if err != nil || !found {
						return errActionLedger
					}
					original.Owner = &reviewAlertOwner{CatalogUID: action.Action.CatalogUID, FileIncarnation: action.Action.FileIncarnation}
				}
				if !reflect.DeepEqual(*row.Alert, original) {
					return errActionLedger
				}
				expected.ReviewConditions++
			}
		}
	}
	if !reflect.DeepEqual(expected, c.Summary) {
		return errActionLedger
	}
	return nil
}

// These indexes are derived during cold recovery, never serialized authority.
func (f *machine) rebuildProjectionReviewIndex() {
	f.projectionReviews = make(map[string]*btree.BTreeG[string])
	for _, m := range f.image.Monitors {
		f.indexProjectionReviews(Monitor{}, m)
	}
}
func (f *machine) indexProjectionReviews(previous, m Monitor) {
	if m.ActionProjection != nil {
		delete(f.projectionReviews, m.ID)
		return
	}
	if f.projectionReviews == nil {
		f.projectionReviews = make(map[string]*btree.BTreeG[string])
	}
	tree := f.projectionReviews[m.ID]
	if tree == nil {
		tree = btree.NewG(32, func(a, b string) bool { return a < b })
		f.projectionReviews[m.ID] = tree
	}
	if previous.Notifications != nil {
		for id := range previous.Notifications.ReviewAlerts {
			if m.Notifications == nil {
				tree.Delete(id)
			} else if _, ok := m.Notifications.ReviewAlerts[id]; !ok {
				tree.Delete(id)
			}
		}
	}
	if m.Notifications != nil {
		for id := range m.Notifications.ReviewAlerts {
			tree.ReplaceOrInsert(id)
		}
	}
}
