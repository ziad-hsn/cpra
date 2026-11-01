package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"
)

func TestNotificationLimitCancelledRetryRestores(t *testing.T) {
	cfg := testConfig(t)
	s, err := Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	at := time.Now().UTC()
	m, a := queuedLimitedNotification(t, s, "retry-cancel", at)
	h, err := s.BeginLocalAction(t.Context(), limitedStart(m, a, identity("retry-scope"), 0, at.Add(time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Finish(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordNotificationFeedback(t.Context(), []Command{{MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: at.Add(2 * time.Second), NotificationFeedback: &NotificationFeedback{}}}); err != nil {
		t.Fatal(err)
	}
	r := submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: at.Add(2 * time.Second), Outcome: "failure", Retryable: true})[0]
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	retry := notificationByColor(t, *r.Monitor, "red", Queued)
	// A retry cannot escape its original scope by omitting the command field.
	s.fsm.mu.Lock()
	s.fsm.image.NotificationLimits[identity("retry-scope")] = NotificationRateState{NotBefore: at.Add(time.Minute)}
	s.fsm.mu.Unlock()
	denied := limitedStart(m, retry, "", 0, retry.NotBefore.Add(time.Millisecond))
	denied.NotificationPermit = nil
	blocked, err := s.BeginLocalAction(t.Context(), denied)
	if !errors.Is(err, ErrNotificationRateLimited) {
		t.Fatal("omitted retry permit bypassed pause", err)
	}
	if blocked != nil {
		if err := blocked.Finish(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	for generation := uint64(2); generation <= 5; generation++ {
		r = submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: generation, Outcome: "success", At: at.Add(time.Duration(generation+1) * time.Second)})[0]
		if r.Err != nil {
			t.Fatal(r.Err)
		}
	}
	row, found, err := s.ActionContext(t.Context(), retry.ID)
	if err != nil || !found || row.State != Cancelled || !row.StartedAt.IsZero() || row.NotificationPermit == nil {
		t.Fatal("retry not cancelled before start", row.State, err)
	}
	if err := s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	restored, found, err := s.ActionContext(t.Context(), retry.ID)
	if err != nil || !found || !reflect.DeepEqual(row, restored) {
		t.Fatal("cancelled retry lost on restore", err)
	}
}

func queuedLimitedNotification(t *testing.T, s *Store, id string, at time.Time) (Monitor, Action) {
	t.Helper()
	m := testMonitor()
	m.ID = id
	m.Policy = lifecyclePolicy()
	m.Policy.Intervention = false
	r := submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &m, At: at})[0]
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	r = submit(t, s, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, Outcome: "failure", At: at.Add(time.Millisecond)})[0]
	if r.Err != nil || r.Monitor == nil {
		t.Fatal(r.Err)
	}
	m = *r.Monitor
	return m, notificationByColor(t, m, "red", Queued)
}
func limitedStart(m Monitor, a Action, scope string, interval time.Duration, at time.Time) Command {
	return Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, NoticeRevision: ActionNoticeRevision(a), At: at, NotificationPermit: &NotificationPermit{Scopes: []NotificationRateScope{{ID: scope, Interval: interval}}}}
}
func TestNotificationLimitsReserveThroughDelayedCompletion(t *testing.T) {
	s := openCatalogMemory(t)
	at := time.Now().UTC()
	scope := identity("shared-channel")
	m, a := queuedLimitedNotification(t, s, "a", at)
	m2, a2 := queuedLimitedNotification(t, s, "b", at)
	h, err := s.BeginLocalAction(t.Context(), limitedStart(m, a, scope, time.Second, at.Add(time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	first, _ := s.Get(m.ID)
	original := first.Actions[a.ID]
	// Delayed grants cannot release a second send just because its requested
	// start timestamp is after the nominal interval.
	blocked, err := s.BeginLocalAction(t.Context(), limitedStart(m2, a2, scope, time.Second, at.Add(5*time.Second)))
	if !errors.Is(err, ErrNotificationRateLimited) {
		t.Fatal("paced scope overlapped", err)
	}
	if blocked != nil {
		if err := blocked.Finish(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	after, _ := s.Get(m2.ID)
	if after.Actions[a2.ID].State != Queued || !after.Actions[a2.ID].Deadline.IsZero() {
		t.Fatal("denied permit consumed execution budget")
	}
	if err := h.Finish(t.Context()); err != nil {
		t.Fatal(err)
	}
	finished := at.Add(10 * time.Second)
	feedback := Command{MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: finished, NotificationFeedback: &NotificationFeedback{Updates: []NotificationRateUpdate{{ID: scope, State: NotificationRateState{NotBefore: finished.Add(30 * time.Second)}}}}}
	for range 2 {
		if err := s.RecordNotificationFeedback(t.Context(), []Command{feedback}); err != nil {
			t.Fatal(err)
		}
	}
	states, err := s.NotificationLimits(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if states[scope].InFlight != 0 || states[scope].NotBefore != finished.Add(30*time.Second) {
		t.Fatal("cooldown or completion accounting changed", states)
	}
	if err := s.fsm.Restore(io.NopCloser(bytes.NewReader(captureSnapshotBytes(t, s.fsm)))); err != nil {
		t.Fatal(err)
	}
	states2, err := s.NotificationLimits(t.Context())
	if err != nil || !reflect.DeepEqual(states, states2) {
		t.Fatal("snapshot lost cooldown", err)
	}
	bad := feedback
	bad.NotificationFeedback = &NotificationFeedback{Updates: []NotificationRateUpdate{{ID: identity("substituted-destination"), State: NotificationRateState{NotBefore: finished.Add(time.Minute)}}}}
	if err := s.RecordNotificationFeedback(t.Context(), []Command{bad}); err == nil {
		t.Fatal("contradictory feedback changed scope")
	}
	current, _ := s.Get(m.ID)
	if current.Actions[a.ID].Deadline != original.Deadline || current.Actions[a.ID].State != Started {
		t.Fatal("pacing rewrote delivery evidence")
	}
	s.fsm.mu.RLock()
	old := s.fsm.image
	old.Version = RecoveryRulesFormatVersion
	s.fsm.mu.RUnlock()
	if validateNotificationLimitsImage(old) == nil {
		t.Fatal("old format accepted pacing")
	}
}

func TestNotificationLimitsAtomicScopesAndOrdinaryReclamation(t *testing.T) {
	s := openCatalogMemory(t)
	at := time.Now().UTC()
	scope := identity("webhook")
	m, a := queuedLimitedNotification(t, s, "ordinary", at)
	h, err := s.BeginLocalAction(t.Context(), limitedStart(m, a, scope, 0, at.Add(time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Finish(t.Context()); err != nil {
		t.Fatal(err)
	}
	feedback := Command{MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: at.Add(2 * time.Second), NotificationFeedback: &NotificationFeedback{}}
	if err := s.RecordNotificationFeedback(t.Context(), []Command{feedback}); err != nil {
		t.Fatal(err)
	}
	states, _ := s.NotificationLimits(t.Context())
	if len(states) != 0 {
		t.Fatal("ordinary completed destination became permanent limiter state")
	}
	m2, a2 := queuedLimitedNotification(t, s, "next", at)
	busy := identity("busy")
	fresh := identity("free")
	s.fsm.mu.Lock()
	s.fsm.image.NotificationLimits[busy] = NotificationRateState{NotBefore: at.Add(time.Hour)}
	s.fsm.mu.Unlock()
	c := limitedStart(m2, a2, fresh, time.Second, at.Add(3*time.Second))
	c.NotificationPermit.Scopes = append(c.NotificationPermit.Scopes, NotificationRateScope{ID: busy})
	h, err = s.BeginLocalAction(t.Context(), c)
	if !errors.Is(err, ErrNotificationRateLimited) {
		t.Fatal(err)
	}
	if h != nil {
		_ = h.Finish(t.Context())
	}
	states, _ = s.NotificationLimits(t.Context())
	if _, exists := states[fresh]; exists {
		t.Fatal("partial multi-scope permit consumed capacity")
	}
	c.NotificationPermit.Scopes[1] = c.NotificationPermit.Scopes[0]
	if validateCommand(c) == nil {
		t.Fatal("duplicate scopes accepted")
	}
}

func TestNotificationLimitScopeCapacityAndRestart(t *testing.T) {
	at := policyTime()
	f := &machine{image: image{NotificationLimits: map[string]NotificationRateState{}}}
	for n := 0; n < MaxNotificationLimitScopes; n++ {
		f.image.NotificationLimits[identity(time.Duration(n).String())] = NotificationRateState{NotBefore: at.Add(time.Hour)}
	}
	if f.notificationLimitCapacity([]string{identity("next")}, at) {
		t.Fatal("scope cap exceeded")
	}
	if !f.notificationLimitCapacity([]string{identity("next")}, at.Add(time.Hour)) || len(f.image.NotificationLimits) != 0 {
		t.Fatal("expired scopes were not reclaimed")
	}
	active := identity("active")
	f.image.NotificationLimits[active] = NotificationRateState{NotBefore: at, InFlight: 1, ReleaseInterval: time.Second}
	f.resetNotificationReservations(at.Add(time.Second))
	if state := f.image.NotificationLimits[active]; state.InFlight != 0 || state.NotBefore != at.Add(2*time.Second) {
		t.Fatal("restart granted an extra burst", state)
	}
}

func TestNotificationPermitSaturationRejectsAtomically(t *testing.T) {
	at := policyTime()
	scope, fresh := identity("saturated"), identity("new")
	f := &machine{image: image{Version: DispatchFormatVersion, NotificationLimits: map[string]NotificationRateState{scope: {NotBefore: at, InFlight: maxLocalExecutors}}}}
	m := testMonitor()
	a := Action{ID: "action", Kind: "code"}
	m.Actions = map[string]Action{a.ID: a}
	c := limitedStart(m, a, fresh, 0, at)
	c.NotificationPermit.Scopes = append(c.NotificationPermit.Scopes, NotificationRateScope{ID: scope})
	r := Result{Allowed: true, Monitor: &m}
	f.admitNotificationPermit(c, &r)
	if !errors.Is(r.Err, ErrNotificationRateLimited) || len(f.image.NotificationLimits) != 1 || f.image.NotificationLimits[scope].InFlight != maxLocalExecutors {
		t.Fatal("saturated admission changed reservations", r.Err)
	}
	if err := validateNotificationLimitsImage(f.image); err != nil {
		t.Fatal("writer boundary cannot be read", err)
	}
	state := f.image.NotificationLimits[scope]
	state.InFlight++
	f.image.NotificationLimits[scope] = state
	if validateNotificationLimitsImage(f.image) == nil {
		t.Fatal("reader accepted an impossible reservation count")
	}
}

func TestNotificationReservationSnapshotConsistency(t *testing.T) {
	s := openCatalogMemory(t)
	at := time.Now().UTC()
	scope := identity("snapshot-active")
	m, a := queuedLimitedNotification(t, s, "snapshot", at)
	h, err := s.BeginLocalAction(t.Context(), limitedStart(m, a, scope, time.Second, at.Add(time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Finish(context.Background()) })
	// The normal snapshot reader validates the ledger as well as projected
	// actions; changing either side cannot silently release a reservation.
	if err := s.fsm.Restore(io.NopCloser(bytes.NewReader(captureSnapshotBytes(t, s.fsm)))); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"missing", "too-small", "too-large", "interval", "orphan"} {
		t.Run(kind, func(t *testing.T) {
			s.fsm.mu.Lock()
			original := s.fsm.image.NotificationLimits[scope]
			changed := original
			switch kind {
			case "missing":
				delete(s.fsm.image.NotificationLimits, scope)
			case "too-small":
				changed.InFlight, changed.ReleaseInterval = 0, 0
			case "too-large":
				changed.InFlight++
			case "interval":
				changed.ReleaseInterval = 0
			case "orphan":
				s.fsm.image.NotificationLimits[identity("orphan")] = NotificationRateState{NotBefore: at, InFlight: 1}
			}
			if kind != "missing" {
				s.fsm.image.NotificationLimits[scope] = changed
			}
			s.fsm.mu.Unlock()
			frozen, err := s.fsm.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			sink := &collectionTestSink{}
			err = frozen.Persist(sink)
			frozen.Release()
			s.fsm.mu.Lock()
			s.fsm.image.NotificationLimits[scope] = original
			delete(s.fsm.image.NotificationLimits, identity("orphan"))
			s.fsm.mu.Unlock()
			if err == nil || !sink.cancelled {
				t.Fatal("corrupt reservation published in snapshot")
			}
		})
	}
}

func TestNotificationRetryRetainsPacingAcrossRestartAndNewPolicy(t *testing.T) {
	cfg := testConfig(t)
	s, err := Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	at, scope := time.Now().UTC(), identity("same-destination")
	m, a := queuedLimitedNotification(t, s, "old-policy", at)
	h, err := s.BeginLocalAction(t.Context(), limitedStart(m, a, scope, time.Second, at.Add(time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Finish(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordNotificationFeedback(t.Context(), []Command{{MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: at.Add(2 * time.Second), NotificationFeedback: &NotificationFeedback{}}}); err != nil {
		t.Fatal(err)
	}
	r := submit(t, s, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: at.Add(2 * time.Second), Outcome: "failure", Retryable: true})[0]
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	retry := notificationByColor(t, *r.Monitor, "red", Queued)
	staleMonitor, staleAction := queuedLimitedNotification(t, s, "old-executor", at)
	stale, err := s.BeginLocalAction(t.Context(), limitedStart(staleMonitor, staleAction, scope, 0, at.Add(3*time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	if err := stale.Finish(t.Context()); err != nil {
		t.Fatal(err)
	}
	// This execution returns but loses its feedback before the restart.
	if err := s.Snapshot(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	start := retry.NotBefore.Add(2 * time.Second)
	// The runtime now has no fixed interval. Its old retry still owns the
	// recorded interval, and a new unpaced notification cannot overlap it.
	h, err = s.BeginLocalAction(t.Context(), limitedStart(m, retry, scope, 0, start))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Finish(context.Background()) })
	newMonitor, newAction := queuedLimitedNotification(t, s, "new-policy", start)
	denied, err := s.BeginLocalAction(t.Context(), limitedStart(newMonitor, newAction, scope, 0, start.Add(time.Second)))
	if denied != nil {
		_ = denied.Finish(t.Context())
	}
	if !errors.Is(err, ErrNotificationRateLimited) {
		t.Fatal("new policy bypassed active old permit", err)
	}
	row, ok, err := s.ActionContext(t.Context(), retry.ID)
	if err != nil || !ok || row.NotificationPermit.Scopes[0].Interval != time.Second {
		t.Fatal("retry permit changed", err)
	}
	// A fresh old-session feedback command must not decrement this session's
	// reservation. Replay of an already recorded digest remains harmless.
	old, ok, err := s.ActionContext(t.Context(), staleAction.ID)
	if err != nil || !ok {
		t.Fatal(err)
	}
	result := submit(t, s, Command{Kind: "notification_feedback", MonitorID: staleMonitor.ID, Revision: staleMonitor.Revision, ActionID: old.ID, ExecutorSession: old.ExecutorSession, At: start.Add(time.Second), NotificationFeedback: &NotificationFeedback{}})[0]
	if !errors.Is(result.Err, errNotificationLimit) {
		t.Fatal("old session released current permit", result.Err)
	}
	states, err := s.NotificationLimits(t.Context())
	if err != nil || states[scope].InFlight != 1 {
		t.Fatal("old feedback changed current reservation", err)
	}
}

func TestNotificationSnapshotReaderRejectsReservationMismatch(t *testing.T) {
	s := openCatalogMemory(t)
	at, scope := time.Now().UTC(), identity("reader-reservation")
	m, a := queuedLimitedNotification(t, s, "reader", at)
	h, err := s.BeginLocalAction(t.Context(), limitedStart(m, a, scope, time.Second, at.Add(time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Finish(context.Background()) })
	snapshot := captureSnapshotBytes(t, s.fsm)
	_, validLedger, err := decodeSnapshot(bytes.NewReader(snapshot), "")
	if err != nil {
		t.Fatal("undamaged snapshot rejected", err)
	}
	if validLedger != nil {
		if err := validLedger.Close(); err != nil {
			t.Fatal(err)
		}
	}
	parts := bytes.SplitN(snapshot, []byte("\n"), 3)
	if len(parts) != 3 {
		t.Fatal("unexpected snapshot framing")
	}
	for _, damage := range []string{"missing", "count", "interval", "orphan"} {
		t.Run(damage, func(t *testing.T) {
			var header image
			if err := json.Unmarshal(parts[1], &header); err != nil {
				t.Fatal(err)
			}
			state := header.NotificationLimits[scope]
			switch damage {
			case "missing":
				delete(header.NotificationLimits, scope)
			case "count":
				state.InFlight++
				header.NotificationLimits[scope] = state
			case "interval":
				state.ReleaseInterval = 0
				header.NotificationLimits[scope] = state
			case "orphan":
				header.NotificationLimits[identity("reader-orphan")] = NotificationRateState{InFlight: 1, NotBefore: at}
			}
			encoded, err := json.Marshal(header)
			if err != nil {
				t.Fatal(err)
			}
			corrupt := bytes.Join([][]byte{parts[0], encoded, parts[2]}, []byte("\n"))
			_, ledger, err := decodeSnapshot(bytes.NewReader(corrupt), "")
			if err == nil {
				if ledger != nil {
					_ = ledger.Close()
				}
				t.Fatal("reader accepted mismatched reservation inventory")
			}
			if !errors.Is(err, errNotificationLimit) {
				t.Fatal("snapshot failed for a different reason", err)
			}
		})
	}
}
