//go:build externaljobs

package persistence

import (
	"reflect"
	"testing"
	"time"
)

func TestActionRetirementProtectsOutstandingWorkerReceipt(t *testing.T) {
	s, authority, request, intent, _ := workerOfferFixture(t, "notification")
	poll, err := s.CommitWorkerPoll(t.Context(), authority, request)
	if err != nil || len(poll.Offers) != 1 {
		t.Fatal("offer", err)
	}
	m, _ := s.Get(intent.MonitorID)
	applyControlRequest(t, s, controlRequest(m, "snooze", "retention-snooze", time.Now().UTC()))
	a := retainedAction(t, s, intent.ActionID)
	if a.State != Cancelled || !a.StartedAt.IsZero() || !ordinaryTerminalAction(a) {
		t.Fatal("fixture must be an ordinary unsent cancellation")
	}
	before, exists, err := s.WorkerExecution(t.Context(), intent.ID)
	if err != nil || !exists {
		t.Fatal("execution", err)
	}
	if _, terminal := before.TerminalReceipt(); terminal || !workerActionDependencyRequired(before) {
		t.Fatal("fixture does not await its authoritative receipt")
	}
	retire := func(at time.Time) {
		t.Helper()
		pin, err := s.fsm.collections.pinActions()
		if err != nil {
			t.Fatal(err)
		}
		row, found, err := pin.Get(t.Context(), a.ID)
		pin.Close()
		if err != nil || !found {
			t.Fatal("retirement input", err)
		}
		result := submit(t, s, Command{Kind: "action_retire", At: at, ActionRetirement: &ActionRetirementCommand{
			Cutoff: at.Add(-actionRetention), Items: []ActionRetirementItem{{ID: a.ID, Sequence: row.Sequence}},
		}})[0]
		if result.Err != nil || !result.Allowed {
			t.Fatal("retirement command", result.Err)
		}
	}
	// Future command time exercises only the committed age rule; no clock,
	// handler, provider, or real ninety-day wait is involved in this fixture.
	ageBoundary := a.FinishedAt.Add(100 * 24 * time.Hour)
	retire(ageBoundary)
	if got := retainedAction(t, s, a.ID); !reflect.DeepEqual(got, a) {
		t.Fatal("retirement changed evidence awaiting worker finalization")
	}
	if _, err := s.FinalizeWorkerExecutions(t.Context(), ageBoundary.Add(time.Second), 100); err != nil {
		t.Fatal(err)
	}
	completed, _, err := s.WorkerExecution(t.Context(), intent.ID)
	if err != nil {
		t.Fatal(err)
	}
	receipt, terminal := completed.TerminalReceipt()
	if !terminal || receipt.Disposition != "finalized" || workerActionDependencyRequired(completed) {
		t.Fatal("unsent worker execution lacks a conclusive no-effect receipt")
	}
	retire(ageBoundary.Add(2 * time.Second))
	if _, found, err := s.Action(a.ID); err != nil || found {
		t.Fatal("dependency-free ordinary action did not retire", err)
	}
	after, found, err := s.WorkerExecution(t.Context(), intent.ID)
	if err != nil || !found || !reflect.DeepEqual(after, completed) {
		t.Fatal("action retirement removed or changed the self-contained receipt", err)
	}
}
