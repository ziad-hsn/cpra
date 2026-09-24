package systems

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/raft"
	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/fleetview"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

type lostOwnerReply struct {
	store       *persistence.Store
	kind        string
	commitFirst bool
	lost        bool
	writes      [][]byte
	holdBarrier atomic.Bool
	barrierSeen chan struct{}
}

func (w *lostOwnerReply) Submit(ctx context.Context, commands []persistence.Command) ([]persistence.Result, error) {
	if len(commands) != 0 && commands[0].Kind == w.kind {
		b, _ := json.Marshal(commands)
		w.writes = append(w.writes, b)
		if !w.lost {
			w.lost = true
			if w.commitFirst {
				if _, err := w.store.Submit(ctx, commands); err != nil {
					return nil, err
				}
			}
			return nil, errors.Join(persistence.ErrCommitUnconfirmed, raft.ErrLeadershipLost)
		}
	}
	return w.store.Submit(ctx, commands)
}

func (w *lostOwnerReply) Flush(ctx context.Context) error {
	if w.barrierSeen != nil {
		select {
		case w.barrierSeen <- struct{}{}:
		default:
		}
	}
	if w.holdBarrier.Load() {
		return raft.ErrNotLeader
	}
	return w.store.Flush(ctx)
}

func recoveryOwner(t *testing.T, count int) (*DurableSystem, chan []jobs.Result, []jobs.Result) {
	t.Helper()
	cfg := runtimeconfig.Default()
	cfg.Storage.Mode, cfg.Storage.BatchSize = "memory", 2
	store, err := persistence.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	w := ecs.NewWorld()
	for i := 0; i < count; i++ {
		ent := releaseMonitor(t, &w, 1, 1)
		state := ecs.NewMap1[components.MonitorState](&w).Get(ent)
		state.MonitorID = fmt.Sprintf("recovery-%d", i)
		ecs.NewMap1[components.PulseConfig](&w).Get(ent).Interval = time.Hour
	}
	ch := make(chan []jobs.Result, 2)
	s := NewDurableSystem(&w, store, cfg, &fleetview.Holder{}, noopLogger{}, &admissionQueue{full: true}, nil, nil, []<-chan []jobs.Result{ch}, time.Minute, false)
	if err := s.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.Initialize(&w)
	now := time.Now()
	s.lastSLO = now
	rows := make([]jobs.Result, 0, count)
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("recovery-%d", i)
		ent := s.entities[id]
		state := s.states.Get(ent)
		r := jobs.Result{ID: uuid.New(), Ent: ent, Type: "pulse", Driver: "http", MonitorID: id, Revision: state.Revision, Generation: 1, Scheduled: now, ExecutionStart: now.Add(time.Millisecond), ExecutionEnd: now.Add(2 * time.Millisecond)}
		s.pending[id], s.pendingCheckID[id] = true, r.ID
		state.SetPulsePending(true)
		store.SLO().Expect(r.Driver, now)
		rows = append(rows, r)
	}
	return s, ch, rows
}

func TestOwnerRecoveryPreservesOriginalResultsAndOversizedSuffix(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprintf("committed_%t", committed), func(t *testing.T) {
			s, ch, rows := recoveryOwner(t, 5)
			writer := &lostOwnerReply{store: s.store, kind: "pulse", commitFirst: committed}
			s.committer = writer
			ch <- rows
			if s.drain() || s.pendingWrite == nil || len(s.resultBacklog) != 3 || s.AdmissionReady() || !s.store.Status().Ready {
				t.Fatal("uncertain prefix/suffix or recoverable admission was lost")
			}
			for _, row := range rows[:2] {
				if !s.pending[row.MonitorID] || s.pendingCheckID[row.MonitorID] != row.ID {
					t.Fatal("invocation ownership released before commit confirmation")
				}
			}
			writer.holdBarrier.Store(true)
			s.Update(s.world)
			if len(writer.writes) != 1 || len(s.resultBacklog) != 3 {
				t.Fatal("uncertain FIFO barrier allowed another write or discarded the suffix")
			}
			writer.holdBarrier.Store(false)
			for i := 0; i < 5; i++ {
				s.nextRecovery = time.Time{}
				s.Update(s.world)
			}
			if s.lastError != nil || s.pendingWrite != nil || s.resultsBuffered() || !s.AdmissionReady() {
				t.Fatalf("owner failed to recover: %v", s.lastError)
			}
			if len(writer.writes) != 4 || !slices.Equal(writer.writes[0], writer.writes[1]) {
				t.Fatal("uncertain command batch changed identity, outcome or timestamp")
			}
			for _, row := range rows {
				m, ok := s.store.Get(row.MonitorID)
				if !ok || m.Generation != 1 || m.TotalChecks != 1 || m.SuccessfulChecks != 1 || s.pending[row.MonitorID] {
					t.Fatalf("result was lost or applied twice: %+v", m)
				}
			}
			view := s.store.SLO().View(time.Now(), time.Minute, 1)
			if len(view.Reports) != 1 || view.Reports[0].Samples != 5 || view.Reports[0].Expected != 5 {
				t.Fatalf("duplicate/no-op replies changed observed SLO accounting: %+v", view)
			}
		})
	}
}

func TestOwnerRecoveryNeverInventsFailureForWithheldCheck(t *testing.T) {
	s, ch, rows := recoveryOwner(t, 1)
	row := rows[0]
	row.ExecutionStart, row.ExecutionEnd, row.Err = time.Time{}, time.Time{}, raft.ErrNotLeader
	ch <- []jobs.Result{row}
	s.Update(s.world)
	m, _ := s.store.Get(row.MonitorID)
	if m.TotalChecks != 0 || m.Incident || m.LastOutcome != "" || s.pending[row.MonitorID] {
		t.Fatalf("uninvoked check became a target observation: %+v", m)
	}
	if s.checks[row.Ent.ID()].ready != row.Scheduled.UnixNano() || s.scheduler.ReadyLen() != 1 {
		t.Fatal("withheld check lost its original scheduled obligation")
	}
	view := s.store.SLO().View(row.Scheduled.Add(20*time.Second), time.Minute, 1)
	if len(view.Reports) != 1 || view.Reports[0].Expected != 1 || view.Reports[0].Samples != 0 {
		t.Fatalf("withheld check improved attainment: %+v", view)
	}
}

func TestOwnerShutdownWaitsForUnconfirmedResults(t *testing.T) {
	s, ch, rows := recoveryOwner(t, 1)
	writer := &lostOwnerReply{store: s.store, kind: "pulse", commitFirst: true, barrierSeen: make(chan struct{}, 1)}
	s.committer = writer
	ch <- rows
	close(ch)
	if s.drain() {
		t.Fatal("fixture did not lose the committed reply")
	}
	s.StopAdmission()
	writer.holdBarrier.Store(true)
	done := make(chan struct{})
	go func() { s.Finalize(s.world); close(done) }()
	select {
	case <-writer.barrierSeen:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not attempt recovery")
	}
	select {
	case <-done:
		t.Fatal("shutdown reported completion with an unresolved write")
	default:
	}
	writer.holdBarrier.Store(false)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("confirmed shutdown did not join")
	}
	m, _ := s.store.Get(rows[0].MonitorID)
	if m.TotalChecks != 1 || s.pendingWrite != nil || s.AdmissionReady() {
		t.Fatal("shutdown lost the outcome or reopened admission")
	}
}

func TestOwnerRecoveryDoesNotClearPermanentFailure(t *testing.T) {
	s, ch, rows := recoveryOwner(t, 1)
	s.recovering.Store(true)
	s.store.MarkUnavailable(errors.New("failed synchronous write"))
	ch <- rows
	s.Update(s.world)
	if s.lastError == nil || s.AdmissionReady() || s.store.Status().Ready || len(ch) != 1 {
		t.Fatal("permanent failure was recovered or discarded accepted results")
	}
}

func TestOwnerSLOCheckpointRetryKeepsOriginalWindow(t *testing.T) {
	s, _, _ := recoveryOwner(t, 1)
	writer := &lostOwnerReply{store: s.store, kind: "slo", commitFirst: true}
	s.committer = writer
	old := s.lastSLO
	at := time.Now()
	s.persistSLO(at)
	if s.pendingWrite == nil || !s.lastSLO.Equal(old) {
		t.Fatal("uncertain checkpoint was acknowledged")
	}
	if !s.recoverWrites(time.Now()) || s.pendingWrite != nil || !s.lastSLO.Equal(at) {
		t.Fatalf("checkpoint did not settle: %v", s.lastError)
	}
	if len(writer.writes) != 2 || !slices.Equal(writer.writes[0], writer.writes[1]) {
		t.Fatal("recovery regenerated the SLO snapshot or its observation time")
	}
}

func TestOwnerNotificationRetryResultLostReplyDoesNotCreateSecondSuccessor(t *testing.T) {
	s, _, rows := recoveryOwner(t, 1)
	row := rows[0]
	at := time.Now().UTC()
	results, err := s.store.Submit(context.Background(), []persistence.Command{{Kind: "pulse", MonitorID: row.MonitorID, Revision: row.Revision, Generation: 1, Outcome: "failure", At: at}})
	if err != nil || len(results) != 1 || results[0].Monitor == nil {
		t.Fatal("fixture pulse failed", err)
	}
	m := *results[0].Monitor
	var action persistence.Action
	for _, candidate := range m.Actions {
		if candidate.Kind == "code" && candidate.Color == "yellow" {
			action = candidate
		}
	}
	if action.ID == "" {
		t.Fatal("fixture has no queued notification")
	}
	handle, err := s.store.BeginLocalAction(context.Background(), persistence.Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: action.ID, At: at})
	if err != nil || handle == nil {
		t.Fatal("fixture notification start failed", err)
	}
	if err := handle.Finish(context.Background()); err != nil {
		t.Fatal(err)
	}
	writer := &lostOwnerReply{store: s.store, kind: "result", commitFirst: true}
	s.committer = writer
	s.admitted[action.ID] = true
	job := jobs.Result{ID: uuid.New(), Type: "code", MonitorID: m.ID, Revision: m.Revision, ActionID: action.ID, ExecutionStart: at, ExecutionEnd: at.Add(time.Millisecond), Err: &jobs.DeliveryError{Retryable: true}}
	if s.commitResults([]jobs.Result{job}) || !s.admitted[action.ID] {
		t.Fatal("uncertain action result lost its ownership")
	}
	before, _ := s.store.Get(m.ID)
	if _, exists := before.Actions[action.ID]; exists {
		t.Fatal("fixture did not replace the rejected notification with a successor")
	}
	if !s.recoverWrites(time.Now()) || s.pendingWrite != nil || s.admitted[action.ID] {
		t.Fatalf("action result did not reconcile: %v", s.lastError)
	}
	after, _ := s.store.Get(m.ID)
	beforeJSON, _ := json.Marshal(before.Actions)
	afterJSON, _ := json.Marshal(after.Actions)
	if !slices.Equal(beforeJSON, afterJSON) || len(writer.writes) != 2 || !slices.Equal(writer.writes[0], writer.writes[1]) {
		t.Fatal("result replay changed action identity or created another successor")
	}
	page, err := s.store.History().Page(m.ID, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	failures := 0
	for _, event := range page.Events {
		if event.ActionID == action.ID && event.Type == "action_failed" {
			failures++
		}
	}
	if failures != 1 {
		t.Fatalf("notification result event count = %d", failures)
	}
}

func TestOwnerResultReadCancellationDoesNotReplayEarlierSLOObservation(t *testing.T) {
	s, _, rows := recoveryOwner(t, 2)
	commands := make([]persistence.Command, len(rows))
	for i, row := range rows {
		commands[i] = persistence.Command{Kind: "pulse", MonitorID: row.MonitorID, Revision: row.Revision, Generation: 1, Outcome: "success", At: time.Now().UTC(), Scheduled: row.Scheduled, ExecutionStart: row.ExecutionStart, ExecutionEnd: row.ExecutionEnd, Driver: row.Driver}
	}
	results, err := s.store.Submit(context.Background(), commands)
	if err != nil || len(results) != 2 {
		t.Fatal(err)
	}
	// The first result is already locally available. A duplicate second result
	// needs a contextual read to prove its original committed observation.
	results[1] = persistence.Result{}
	p := &ownerWrite{kind: resultWrite, commands: commands, observations: rows, monitorUIDs: []string{"", ""}, results: results, confirmed: true}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.finishResultWrite(ctx, p); !errors.Is(err, context.Canceled) || p.completed != 1 {
		t.Fatalf("canceled read lost partial completion: cursor=%d err=%v", p.completed, err)
	}
	if s.pending[rows[0].MonitorID] || !s.pending[rows[1].MonitorID] {
		t.Fatal("partial result ownership was not retained")
	}
	if err := s.finishResultWrite(context.Background(), p); err != nil || p.completed != 2 {
		t.Fatal("remaining result did not reconcile", err)
	}
	if err := s.finishResultWrite(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	view := s.store.SLO().View(time.Now(), time.Minute, 1)
	if len(view.Reports) != 1 || view.Reports[0].Samples != 2 {
		t.Fatalf("local replay counted an earlier result twice: %+v", view)
	}
}

func TestOwnerAdmissionCancellationDoesNotBecomeTargetTimeout(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, errors.Join(errAdmissionRecovering, context.DeadlineExceeded)} {
		t.Run(err.Error(), func(t *testing.T) {
			s, _, rows := recoveryOwner(t, 1)
			row := rows[0]
			row.Err, row.ExecutionStart, row.ExecutionEnd = err, time.Time{}, time.Time{}
			if !s.commitResults([]jobs.Result{row}) {
				t.Fatal("withheld admission was treated as an uncertain provider result")
			}
			m, _ := s.store.Get(row.MonitorID)
			if m.TotalChecks != 0 || m.Incident || s.checks[row.Ent.ID()].ready != row.Scheduled.UnixNano() {
				t.Fatalf("uninvoked check produced a failure or lost its schedule: %+v", m)
			}
		})
	}
}
