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
	if a.Resolution != nil {
		n := *a.Resolution
		a.Resolution = &n
	}
	if a.ReviewNotice != nil {
		v := *a.ReviewNotice
		a.ReviewNotice = &v
	}
	if a.Control != nil {
		v := *a.Control
		a.Control = &v
	}
	if a.AgentConnection != nil {
		v := a.AgentConnection.Clone()
		a.AgentConnection = &v
	}
	if a.RecoveryFailure != nil {
		v := *a.RecoveryFailure
		a.RecoveryFailure = &v
	}
	if a.AgentReceipt != nil {
		v := *a.AgentReceipt
		a.AgentReceipt = &v
	}
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
	if e.Resolution != nil {
		n := *e.Resolution
		e.Resolution = &n
	}
	if e.ReviewNotice != nil {
		copy := *e.ReviewNotice
		e.ReviewNotice = &copy
	}
	if e.Control != nil {
		v := *e.Control
		e.Control = &v
	}
	if e.RecoveryFailure != nil {
		v := *e.RecoveryFailure
		e.RecoveryFailure = &v
	}
	if e.AgentReceipt != nil {
		v := *e.AgentReceipt
		e.AgentReceipt = &v
	}
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
	reader          *actionReader
	tree, byMonitor *btree.BTreeG[actionItem]
	session         string
	Index           uint64
}

func (f *machine) rebuildActionIndexes() {
	_ = f.rebuildActionIndexesContext(context.Background())
}

func (f *machine) rebuildActionIndexesContext(ctx context.Context) (err error) {
	f.actionIndex = btree.NewG(32, func(a, b actionItem) bool { return a.key < b.key })
	f.actionsByMonitor = btree.NewG(32, func(a, b actionItem) bool { return a.key < b.key })
	defer func() {
		if err != nil {
			f.actionIndex, f.actionsByMonitor = nil, nil
		}
	}()
	for id, m := range f.image.Monitors {
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, a := range m.Actions {
			if err := ctx.Err(); err != nil {
				return err
			}
			f.indexAction(id, a)
		}
	}
	return ctx.Err()
}
func (f *machine) indexAction(id string, a Action) {
	if f.image.ActionStorage != nil && a.State != Queued && a.State != Started {
		f.actionIndex.Delete(actionItem{key: a.ID})
		f.actionsByMonitor.Delete(actionItem{key: id + "\x00" + a.ID})
		return
	}
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
	a := item.action
	return ActionRecord{Action: a.Clone(), MonitorID: item.monitorID, ReviewRevision: ActionReviewRevision(a), Held: a.Held(), ExecutorFenced: actionExecutorFenced(a, session),
		LocalExecutorActive: (a.ExecutorKind == "local" || a.ExecutorKind == "agent") && a.ExecutorSession == session && a.ExecutorFinishedAt.IsZero()}
}
func (s *Store) ActionSnapshot() (ActionView, error) {
	return s.ActionSnapshotContext(context.Background())
}
func (s *Store) ActionSnapshotContext(ctx context.Context) (ActionView, error) {
	unlock, err := s.lockControllerState(ctx, true)
	if err != nil {
		return ActionView{}, err
	}
	defer unlock()
	return s.actionSnapshotLocked(ctx)
}

func (s *Store) actionSnapshotLocked(ctx context.Context) (ActionView, error) {
	f := s.fsm
	if f.err != nil {
		return ActionView{}, f.err
	}
	if f.image.ActionStorage != nil && f.image.ActionStorage.Sequence != 0 {
		if f.collections == nil {
			return ActionView{}, errActionLedger
		}
		pin, err := f.collections.pinActionsContext(ctx)
		if err != nil {
			return ActionView{}, err
		}
		if pin.sequence != f.image.ActionStorage.Sequence {
			_ = pin.Close()
			return ActionView{}, errActionLedger
		}
		return ActionView{reader: newActionReader(pin), session: f.image.LocalExecutorSession, Index: f.image.Index}, nil
	}
	if f.actionIndex == nil {
		if err := f.rebuildActionIndexesContext(ctx); err != nil {
			return ActionView{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return ActionView{}, err
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
	f := s.fsm
	if f.image.ActionStorage != nil && f.image.ActionStorage.Sequence != 0 {
		view, err := s.actionSnapshotLocked(ctx)
		unlock()
		if err != nil {
			return ActionRecord{}, false, err
		}
		defer view.Close()
		return view.GetContext(ctx, id)
	}
	defer unlock()
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

// Retain transfers an independent close responsibility to another reader.
func (v ActionView) Retain() (ActionView, error) {
	if v.reader != nil {
		var err error
		v.reader, err = v.reader.retain()
		return v, err
	}
	return v, nil
}

func (v ActionView) Close() error { return v.reader.close() }

func (v ActionView) Get(id string) (ActionRecord, bool, error) {
	return v.GetContext(context.Background(), id)
}

func (v ActionView) GetContext(ctx context.Context, id string) (ActionRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return ActionRecord{}, false, err
	}
	if v.reader != nil {
		row, found, err := v.reader.get(ctx, id)
		if err != nil || !found {
			return ActionRecord{}, found, err
		}
		return actionRecord(actionItem{monitorID: row.MonitorID, action: row.Action}, v.session), true, nil
	}
	if v.tree == nil {
		return ActionRecord{}, false, nil
	}
	item, ok := v.tree.Get(actionItem{key: id})
	if !ok {
		return ActionRecord{}, false, nil
	}
	return actionRecord(item, v.session), true, nil
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
	return v.PageContext(context.Background(), id, after, limit)
}

func (v ActionView) PageContext(ctx context.Context, id, after string, limit int) ([]ActionRecord, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if id != "" && !catalogIdentifier(id, 256) {
		return nil, "", errors.New("invalid action monitor")
	}
	if v.reader != nil {
		rows, next, err := v.reader.page(ctx, id, after, limit)
		if err != nil {
			return nil, "", err
		}
		out := make([]ActionRecord, 0, len(rows))
		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				return nil, "", err
			}
			out = append(out, actionRecord(actionItem{monitorID: row.MonitorID, action: row.Action}, v.session))
		}
		return out, next, nil
	}
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
		if ctx.Err() != nil {
			return false
		}
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
	return rows, next, ctx.Err()
}
