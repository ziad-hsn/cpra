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
	m, _ := s.Get(in.MonitorID)
	if m.Actions[in.ActionID].State != Cancelled {
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
				m, _ := s.Get(in.MonitorID)
				if m.Actions[in.ActionID].State != Cancelled {
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
	s := openCatalogMemory(t)
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
	if current.Actions[in.ActionID].State != Cancelled || current.Actions[in.ActionID].Deadline != original.Deadline || len(current.Actions) != len(monitor.Actions) {
		t.Fatal("restart renewed or replaced expired intent")
	}
	record, ok, err := s.WorkerExecution(t.Context(), in.ID)
	if err != nil || !ok || record.Lifecycle.GrantID != "" {
		t.Fatal("unexpected executable grant", err)
	}
}
