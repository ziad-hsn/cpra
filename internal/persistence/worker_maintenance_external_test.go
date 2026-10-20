//go:build externaljobs

package persistence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func maintenanceEvaluator(active, covered bool) WorkerMaintenanceEvaluator {
	return func(context.Context, WorkerExecutionIntent, time.Time, time.Time) (WorkerMaintenanceDecision, error) {
		return WorkerMaintenanceDecision{Available: true, Active: active, SinceQueued: covered}, nil
	}
}

func TestWorkerMaintenancePollPublishesCancellation(t *testing.T) {
	s, a, poll, in, _ := workerOfferFixture(t, "notification")
	view, err := s.ControlSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	response, err := s.CommitWorkerPoll(t.Context(), a, poll, maintenanceEvaluator(true, true))
	if err != nil || len(response.Offers) != 0 {
		t.Fatal("suppressed notification offered", err)
	}
	if retainedAction(t, s, in.ActionID).State != Cancelled {
		t.Fatal("covered notification not cancelled")
	}
	changes, err := s.ControlsChangedSince(view.Cursor, 100)
	if err != nil || len(changes.Changes) == 0 {
		t.Fatal("controller did not receive cancellation", err)
	}
}

func TestWorkerMaintenanceStartReevaluatesOffer(t *testing.T) {
	for _, category := range []string{"check", "recovery", "notification"} {
		t.Run(category, func(t *testing.T) {
			s, a, poll, in, _ := workerOfferFixture(t, category)
			response, err := s.CommitWorkerPoll(t.Context(), a, poll, maintenanceEvaluator(false, false))
			if err != nil || len(response.Offers) != 1 {
				t.Fatal("initial offer", err)
			}
			result, err := s.CommitWorkerStart(t.Context(), a, workerStartFromOffer(response), maintenanceEvaluator(true, true))
			if err != nil {
				t.Fatal(err)
			}
			want := "rejected"
			if category == "check" {
				want = "granted"
			}
			if result.Disposition != want {
				t.Fatalf("maintenance start = %s; want %s", result.Disposition, want)
			}
			if category == "notification" {
				if retainedAction(t, s, in.ActionID).State != Cancelled {
					t.Fatal("covered notification was kept for deferred delivery")
				}
			}
		})
	}
}

func TestWorkerMaintenanceMissingDecisionAndConfigurationPin(t *testing.T) {
	for _, missing := range []bool{true, false} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			s, a, poll, in, _ := workerOfferFixture(t, "recovery")
			response, err := s.CommitWorkerPoll(t.Context(), a, poll)
			if err != nil || len(response.Offers) != 1 {
				t.Fatal(err)
			}
			at := time.Now().UTC()
			cmd := WorkerStartCommand{NodeID: s.nodeID, OwnerEpoch: s.executorSession, Authority: a,
				Request: workerStartFromOffer(response), ProposedGrantID: uuid.NewString()}
			if !missing {
				d := workerMaintenanceIdentity(in, at)
				d.Available, d.MonitorRevision = true, "wrong-revision"
				cmd.Maintenance = &d
			}
			result := submit(t, s, Command{Kind: "worker_start", At: at, commandExtensions: commandExtensions{WorkerStart: &cmd}})[0]
			if result.WorkerStart == nil || result.WorkerStart.Disposition != "rejected" {
				t.Fatal("unprepared start admitted", result.Err)
			}
			m, _ := s.Get(in.MonitorID)
			if m.Actions[in.ActionID].State != Queued {
				t.Fatal("missing decision changed provider facts")
			}
		})
	}
}

func TestWorkerMaintenancePeriodicPolicyRequiresEvaluator(t *testing.T) {
	s, a, request, in, _ := workerOfferFixture(t, "recovery")
	m, _ := s.Get(in.MonitorID)
	config := Monitor{ID: m.ID, Name: m.Name, Revision: m.Revision, CatalogUID: m.CatalogUID, Policy: m.Policy.Clone()}
	config.Policy.PeriodicMaintenance = true
	submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &config, Guard: &in.Guard, At: time.Now().UTC()})
	response, err := s.CommitWorkerPoll(t.Context(), a, request)
	if err != nil || len(response.Offers) != 0 {
		t.Fatal("missing evaluator admitted periodic work", err)
	}
	request.SessionID, request.PollSequence = response.SessionID, 2
	response, err = s.CommitWorkerPoll(t.Context(), a, request, maintenanceEvaluator(false, false))
	if err != nil || len(response.Offers) != 1 {
		t.Fatal("prepared periodic work unavailable", err)
	}
	if err := s.fsm.Restore(io.NopCloser(bytes.NewReader(captureSnapshotBytes(t, s.fsm)))); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerMaintenanceResultPreservesHealthAndReceipt(t *testing.T) {
	for _, mode := range []string{"active", "unavailable", "catalog-changed"} {
		t.Run(mode, func(t *testing.T) {
			s, a, start, input := workerResultFixture(t, "check", "failure")
			record, _, _ := s.WorkerExecution(t.Context(), input.ExecutionID)
			m, _ := s.Get(record.Intent.MonitorID)
			config := Monitor{ID: m.ID, Name: m.Name, Revision: m.Revision, CatalogUID: m.CatalogUID, Policy: m.Policy.Clone()}
			config.Policy.Intervention = true
			submit(t, s, Command{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, Config: &config, Guard: &record.Intent.Guard, At: time.Now().UTC()})
			evaluate := maintenanceEvaluator(true, false)
			if mode == "unavailable" {
				evaluate = func(context.Context, WorkerExecutionIntent, time.Time, time.Time) (WorkerMaintenanceDecision, error) {
					return WorkerMaintenanceDecision{}, errors.New("temporary policy read failure")
				}
			} else if mode == "catalog-changed" {
				evaluate = func(context.Context, WorkerExecutionIntent, time.Time, time.Time) (WorkerMaintenanceDecision, error) {
					old := requireCatalog(t, s, CatalogKey{Kind: "Monitor", ID: m.ID})
					mutation := updateCatalogMutation(t, s, old, uuid.NewString(), "new-periodic-policy", old.References...)
					if result := catalogSubmit(t, s, mutation); result.Err != nil {
						t.Fatal(result.Err)
					}
					return WorkerMaintenanceDecision{Available: true}, nil
				}
			}
			receipt, err := s.CommitWorkerResult(t.Context(), a, input, evaluate)
			if err != nil || receipt.Disposition != "accepted" {
				t.Fatal("known result lost", err)
			}
			m, _ = s.Get(m.ID)
			if m.LastOutcome != "failure" || m.InterventionAttempts != 0 || len(m.Actions) != 0 {
				t.Fatal("maintenance lost health or admitted follow-up work")
			}
			never := func(context.Context, WorkerExecutionIntent, time.Time, time.Time) (WorkerMaintenanceDecision, error) {
				t.Fatal("terminal operation consulted current configuration")
				return WorkerMaintenanceDecision{}, nil
			}
			duplicate, err := s.CommitWorkerResult(t.Context(), a, input, never)
			if err != nil || duplicate != receipt {
				t.Fatal("original receipt not returned", err)
			}
			start.Mode = "reconcile"
			terminal, err := s.CommitWorkerStart(t.Context(), a, start, never)
			if err != nil || terminal.Disposition != "terminal" || terminal.ReceiptID != receipt.ReceiptID {
				t.Fatal("terminal reconcile lost", err)
			}
		})
	}
}

func TestWorkerMaintenancePollScanProgressAndConcurrency(t *testing.T) {
	s := openWorkerMemory(t)
	a, request, types := workerFairnessFixture(t, s)
	for n := range 101 {
		commitWorkerQueueItem(t, s, types[1], fmt.Sprintf("suppressed-%03d", n))
	}
	healthy := commitWorkerQueueItem(t, s, types[0], "healthy-check")
	response, err := s.CommitWorkerPoll(t.Context(), a, request, maintenanceEvaluator(true, true))
	if err != nil || len(response.Offers) != 0 {
		t.Fatal("suppressed first page", err)
	}
	request.SessionID, request.PollSequence = response.SessionID, 2
	response, err = s.CommitWorkerPoll(t.Context(), a, request, maintenanceEvaluator(true, true))
	if err != nil || len(response.Offers) != 1 || response.Offers[0].ExecutionID != healthy.Intent.ID {
		t.Fatal("suppressed entries starved later work", err)
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			command := WorkerSessionCommand{SelectionVersion: 2, Authority: a, Request: request, OwnerEpoch: s.executorSession}
			_, err := s.prepareWorkerPollMaintenance(t.Context(), command, time.Now().UTC(), []WorkerMaintenanceEvaluator{maintenanceEvaluator(true, true)})
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}

func TestWorkerMaintenancePreparationStopsAtAvailableCapacity(t *testing.T) {
	for _, limits := range [][2]int{{1, 1}, {10, 1}, {3, 3}} {
		t.Run(fmt.Sprint(limits), func(t *testing.T) {
			s := openWorkerMemory(t)
			a, request, types := workerFairnessFixture(t, s)
			request.Capacity, request.Limit = limits[0], limits[1]
			var queued []WorkerExecutionRecord
			for n := range 8 {
				queued = append(queued, commitWorkerQueueItem(t, s, types[0], fmt.Sprintf("ready-%d", n)))
			}
			calls := 0
			evaluate := func(context.Context, WorkerExecutionIntent, time.Time, time.Time) (WorkerMaintenanceDecision, error) {
				calls++
				return WorkerMaintenanceDecision{Available: true}, nil
			}
			response, err := s.CommitWorkerPoll(t.Context(), a, request, evaluate)
			limit := min(request.Capacity, request.Limit)
			if err != nil || len(response.Offers) != limit || calls != limit {
				t.Fatalf("poll prepared work beyond usable capacity: offers=%d evaluations=%d want=%d err=%v", len(response.Offers), calls, limit, err)
			}
			for n, offer := range response.Offers {
				if offer.ExecutionID != queued[n].Intent.ID {
					t.Fatal("capacity bound changed committed execution order")
				}
			}
		})
	}
}

func TestWorkerMaintenanceOlderPreparationCannotReplaceNewerCursor(t *testing.T) {
	s := openWorkerMemory(t)
	a, request, types := workerFairnessFixture(t, s)
	var oldest string
	for n := range 102 {
		r := commitWorkerQueueItem(t, s, types[1], fmt.Sprintf("recovery-%03d", n))
		if n == 0 {
			oldest = r.Intent.ID
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	var prepareErr error
	command := WorkerSessionCommand{SelectionVersion: 2, Authority: a, Request: request, OwnerEpoch: s.executorSession}
	go func() {
		defer close(done)
		_, prepareErr = s.prepareWorkerPollMaintenance(ctx, command, time.Now().UTC(), []WorkerMaintenanceEvaluator{
			func(context.Context, WorkerExecutionIntent, time.Time, time.Time) (WorkerMaintenanceDecision, error) {
				once.Do(func() {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
					}
				})
				return WorkerMaintenanceDecision{Available: true, Active: true}, ctx.Err()
			},
		})
	}()
	t.Cleanup(func() { cancel(); <-done })
	select {
	case <-entered:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	// A newer preparation fills its capacity and resets the position to zero.
	// The old preparation must not publish its later all-suppressed offset.
	if _, err := s.prepareWorkerPollMaintenance(t.Context(), command, time.Now().UTC(), []WorkerMaintenanceEvaluator{maintenanceEvaluator(false, false)}); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-done
	if prepareErr != nil {
		t.Fatal(prepareErr)
	}
	response, err := s.CommitWorkerPoll(t.Context(), a, request, maintenanceEvaluator(false, false))
	if err != nil || len(response.Offers) != 1 || response.Offers[0].ExecutionID != oldest {
		t.Fatal("an older preparation skipped the oldest eligible execution", err)
	}
}

func TestWorkerMaintenanceRotatedPageRespectsOfferByteBudget(t *testing.T) {
	s := openWorkerMemory(t)
	a, request, types := workerFairnessFixture(t, s)
	request.Capacity, request.Limit = 81, 81
	var queue []string
	for n := range 84 {
		wireKiB := 49
		if n == 0 {
			wireKiB = 160
		} else if n < 4 {
			wireKiB = 33
		}
		in := workerQueueIntent(t, s, types[0], fmt.Sprintf("wire-%03d", n))
		var err error
		// Account for the GCM tag so the fixture has exact response costs.
		in.Payload, err = catalogSealer(t).Seal(t.Context(), in.Binding(s.nodeID), bytes.Repeat([]byte("p"), (wireKiB<<10)-workerOfferWireOverhead-16))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.CommitWorkerExecution(t.Context(), in); err != nil {
			t.Fatal(err)
		}
		queue = append(queue, in.ID)
	}
	// A prior suppressed page can rotate preparation away from committed order.
	s.workerMaintenanceMu.Lock()
	s.workerMaintenanceScan = map[string]*workerMaintenanceCursor{a.WorkerUID: {position: 4}}
	s.workerMaintenanceMu.Unlock()
	response, err := s.CommitWorkerPoll(t.Context(), a, request, maintenanceEvaluator(false, false))
	if err != nil || len(response.Offers) != 81 {
		t.Fatalf("prepared capacity disagreed with committed byte selection: offers=%d err=%v", len(response.Offers), err)
	}
	// The 160-KiB row cannot fit alongside the selected 80 suffix rows. All
	// emitted decisions must fit together when Apply restores committed order.
	want := append([]string{queue[1]}, queue[4:]...)
	for n, offer := range response.Offers {
		if offer.ExecutionID != want[n] {
			t.Fatal("rotated preparation reordered selected execution identities")
		}
	}
	request.SessionID, request.PollSequence = response.SessionID, 2
	response, err = s.CommitWorkerPoll(t.Context(), a, request, maintenanceEvaluator(false, false))
	if err != nil || len(response.Offers) != 3 || response.Offers[0].ExecutionID != queue[0] {
		t.Fatal("byte-limited preparation lost remaining queued work", err)
	}
}

func TestWorkerRecoveryRestartCannotRenewExpiredIntent(t *testing.T) {
	s, authority, poll, in, _ := workerOfferFixture(t, "recovery")
	offered, err := s.CommitWorkerPoll(t.Context(), authority, poll)
	if err != nil || len(offered.Offers) != 1 {
		t.Fatal("initial offer", err)
	}
	monitor, _ := s.Get(in.MonitorID)
	original := monitor.Actions[in.ActionID]
	if err := s.fsm.Restore(io.NopCloser(bytes.NewReader(captureSnapshotBytes(t, s.fsm)))); err != nil {
		t.Fatal(err)
	}
	submit(t, s, Command{Kind: "recover", At: original.Deadline})
	submit(t, s, Command{Kind: "pulse", MonitorID: monitor.ID, Revision: monitor.Revision, Guard: &in.Guard, Generation: monitor.Generation + 1, Outcome: "failure", ExecutionStart: original.Deadline.Add(time.Millisecond), At: original.Deadline.Add(time.Second)})
	result := workerStartAt(t, s, authority, workerStartFromOffer(offered), original.Deadline.Add(2*time.Second))
	if result.WorkerStart != nil && result.WorkerStart.Disposition == "granted" {
		t.Fatal("expired restored recovery was executable")
	}
	current, _ := s.Get(in.MonitorID)
	if retainedAction(t, s, in.ActionID).State != Cancelled || retainedAction(t, s, in.ActionID).Deadline != original.Deadline || len(current.Actions) != len(monitor.Actions)-1 {
		t.Fatal("restart renewed or replaced expired intent")
	}
	record, ok, err := s.WorkerExecution(t.Context(), in.ID)
	if err != nil || !ok || record.Lifecycle.GrantID != "" {
		t.Fatal("unexpected executable grant", err)
	}
}
