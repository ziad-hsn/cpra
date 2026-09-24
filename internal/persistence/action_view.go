package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/google/btree"
)

func (a Action) Clone() Action {
	if a.Review != nil {
		r := *a.Review
		r.EvidenceRefs = append([]string(nil), r.EvidenceRefs...)
		a.Review = &r
	}
	if a.LateEvidence != nil {
		e := *a.LateEvidence
		a.LateEvidence = &e
	}
	if a.ConflictingEvidence != nil {
		e := *a.ConflictingEvidence
		a.ConflictingEvidence = &e
	}
	return a
}
func (a Action) Held() bool {
	return a.State == Unknown && (a.Review == nil || a.Review.Conflict || a.Review.Resolution == "inconclusive")
}

// ActionReviewRevision binds review to the complete observed action, including
// lifecycle and provider evidence. It never reuses the execution revision as CAS.
func ActionReviewRevision(a Action) string {
	data, _ := json.Marshal(a)
	return identity("action-observation/" + string(data))
}
func (e Event) Clone() Event {
	if e.CollectionExecutionHistory != nil {
		v := e.CollectionExecutionHistory.clone()
		e.CollectionExecutionHistory = &v
	}
	if e.CollectionExecution != nil {
		v := e.CollectionExecution.Clone()
		e.CollectionExecution = &v
	}
	if e.CollectionValidation != nil {
		v := e.CollectionValidation.clone()
		e.CollectionValidation = &v
	}
	e.EvidenceRefs = append([]string(nil), e.EvidenceRefs...)
	if e.Collection != nil {
		r := e.Collection.Clone()
		e.Collection = &r
	}
	if e.Operation != nil {
		r := *e.Operation
		e.Operation = &r
	}
	return e
}

type actionItem struct {
	key       string
	monitorID string
	action    Action
}
type ActionView struct {
	tree, byMonitor *btree.BTreeG[actionItem]
	session         string
	Index           uint64
}

func (f *machine) rebuildActionIndexes() {
	f.actionIndex = btree.NewG(32, func(a, b actionItem) bool { return a.key < b.key })
	f.actionsByMonitor = btree.NewG(32, func(a, b actionItem) bool { return a.key < b.key })
	for id, m := range f.image.Monitors {
		for _, a := range m.Actions {
			f.indexAction(id, a)
		}
	}
}
func (f *machine) indexAction(id string, a Action) {
	f.actionIndex.ReplaceOrInsert(actionItem{key: a.ID, monitorID: id, action: a})
	f.actionsByMonitor.ReplaceOrInsert(actionItem{key: id + "\x00" + a.ID, monitorID: id, action: a})
}
func (f *machine) updateActionIndexes(previous, m Monitor) {
	if f.actionIndex == nil {
		f.rebuildActionIndexes()
	}
	for id := range previous.Actions {
		if _, exists := m.Actions[id]; !exists {
			f.actionIndex.Delete(actionItem{key: id})
			f.actionsByMonitor.Delete(actionItem{key: m.ID + "\x00" + id})
		}
	}
	for id, a := range m.Actions {
		if old, ok := previous.Actions[id]; ok && reflect.DeepEqual(old, a) {
			continue
		}
		f.indexAction(m.ID, a)
	}
}
func actionRecord(item actionItem, session string) ActionRecord {
	return ActionRecord{Action: item.action.Clone(), MonitorID: item.monitorID, ReviewRevision: ActionReviewRevision(item.action), Held: item.action.Held(), ExecutorFenced: actionExecutorFenced(item.action, session)}
}
func (s *Store) ActionSnapshot() (ActionView, error) {
	if err := s.controlReadReady(); err != nil {
		return ActionView{}, err
	}
	s.fsm.mu.Lock()
	defer s.fsm.mu.Unlock()
	f := s.fsm
	if f.err != nil {
		return ActionView{}, f.err
	}
	if f.actionIndex == nil {
		f.rebuildActionIndexes()
	}
	return ActionView{tree: f.actionIndex.Clone(), byMonitor: f.actionsByMonitor.Clone(), session: f.image.LocalExecutorSession, Index: f.image.Index}, nil
}
func (s *Store) Action(id string) (ActionRecord, bool, error) {
	return s.ActionContext(context.Background(), id)
}

// ActionContext reads one action without creating a fleet-wide snapshot.
func (s *Store) ActionContext(ctx context.Context, id string) (ActionRecord, bool, error) {
	unlock, err := s.lockControllerState(ctx, false)
	if err != nil {
		return ActionRecord{}, false, err
	}
	defer unlock()
	f := s.fsm
	var item actionItem
	var ok bool
	if f.actionIndex != nil {
		item, ok = f.actionIndex.Get(actionItem{key: id})
	} else {
		// Restores and committed monitor updates build the index. The fallback
		// also supports an unindexed initial image, with cancellation per row.
		for monitorID, m := range f.image.Monitors {
			if err := ctx.Err(); err != nil {
				return ActionRecord{}, false, err
			}
			if a, exists := m.Actions[id]; exists {
				item, ok = actionItem{key: id, monitorID: monitorID, action: a}, true
				break
			}
		}
	}
	if !ok {
		return ActionRecord{}, false, ctx.Err()
	}
	r := actionRecord(item, f.image.LocalExecutorSession)
	if err := ctx.Err(); err != nil {
		return ActionRecord{}, false, err
	}
	return r, ok, nil
}
func (v ActionView) Get(id string) (ActionRecord, bool) {
	if v.tree == nil {
		return ActionRecord{}, false
	}
	item, ok := v.tree.Get(actionItem{key: id})
	if !ok {
		return ActionRecord{}, false
	}
	return actionRecord(item, v.session), true
}
func (v ActionView) Page(after string, limit int) ([]ActionRecord, string, error) {
	return v.page("", after, limit)
}
func (v ActionView) PageByMonitor(id, after string, limit int) ([]ActionRecord, string, error) {
	if !catalogIdentifier(id, 256) {
		return nil, "", errors.New("invalid action monitor")
	}
	return v.page(id, after, limit)
}
func (v ActionView) page(id, after string, limit int) ([]ActionRecord, string, error) {
	if v.tree == nil || limit < 1 || limit > 500 || (after != "" && !catalogIdentifier(after, 256)) {
		return nil, "", errors.New("invalid action page")
	}
	tree, prefix := v.tree, ""
	if id != "" {
		tree, prefix = v.byMonitor, id+"\x00"
	}
	start := prefix + after
	rows := make([]ActionRecord, 0, limit)
	next := ""
	tree.AscendGreaterOrEqual(actionItem{key: start}, func(item actionItem) bool {
		if prefix != "" && !strings.HasPrefix(item.key, prefix) {
			return false
		}
		if item.key == start {
			return true
		}
		if len(rows) == limit {
			next = rows[len(rows)-1].ID
			return false
		}
		rows = append(rows, actionRecord(item, v.session))
		return true
	})
	return rows, next, nil
}
