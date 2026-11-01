//go:build externaljobs

package persistence

import (
	"fmt"
	"testing"
	"time"
)

func TestWorkerFinalizationDeadlineClassification(t *testing.T) {
	for _, version := range []int{ActionFormatVersion, DispatchFormatVersion} {
		for _, scenario := range []string{"deadline", "offer_only", "offer_limit", "owner_lost", "started"} {
			t.Run(fmt.Sprintf("%d/%s", version, scenario), func(t *testing.T) {
				at := policyTime().Add(time.Minute)
				m := conditionMonitor(t)
				m.ActionVersion, m.CatalogUID = version, "original-owner"
				a := Action{ID: "original-action", Kind: "intervention", State: Queued, CatalogUID: m.CatalogUID, Revision: m.Revision, Deadline: at}
				r := WorkerExecutionRecord{Intent: WorkerExecutionIntent{MonitorID: m.ID, MonitorUID: m.CatalogUID, MonitorRevision: m.Revision, ActionID: a.ID, Category: "recovery"}, Finalization: &WorkerFinalization{Reason: "deadline"}}
				switch scenario {
				case "offer_only":
					a.Deadline = time.Time{}
					a.ExecutionBudget = time.Minute
				case "offer_limit", "owner_lost":
					r.Finalization.Reason = scenario
				case "started":
					a.State, a.ExecutorKind, a.ExecutorSession = Started, "worker", "original-grant"
					r.Lifecycle = &WorkerExecutionLifecycle{GrantID: a.ExecutorSession}
				}
				m.Actions = map[string]Action{a.ID: a}
				f := machine{image: image{Monitors: map[string]Monitor{m.ID: m}}}
				after, events := f.workerFinalizationTransition(r, at)
				if after == nil || len(events) != 1 {
					t.Fatal("finalization did not retain original action", events)
				}
				got := after.Actions[a.ID]
				wantReason := ""
				if version == DispatchFormatVersion && scenario == "deadline" {
					wantReason = "execution_deadline"
				}
				wantState := Cancelled
				if scenario == "started" {
					wantState, wantReason = Unknown, "outcome_unknown"
				}
				if got.State != wantState || got.TerminationReason != wantReason || got.Deadline != a.Deadline || events[0].TerminationReason != wantReason {
					t.Fatal("offer, execution budget or historical meaning changed", got, events)
				}
				metrics := new(actionMetrics)
				metrics.record(events)
				wantCount := uint64(0)
				if wantReason == "execution_deadline" {
					wantCount = 1
				}
				if metrics.pipelines[1].executionDeadline.Load() != wantCount {
					t.Fatal("offer or unknown effect counted as execution exhaustion")
				}
			})
		}
	}
}

func TestWorkerExpiryCountsCommittedCancellationOnce(t *testing.T) {
	for _, category := range []string{"recovery", "notification"} {
		t.Run(category, func(t *testing.T) {
			s := openCatalogMemory(t)
			in := workerExpiryFixture(t, s, category)
			for range 3 {
				if err := s.ExpireWorkerAction(t.Context(), in); err != nil {
					t.Fatal(err)
				}
			}
			n := 0
			if category == "recovery" {
				n = 1
			}
			got := s.ActionMetrics()[n]
			if got.Cancelled != 1 || got.ExecutionDeadline != 0 || got.AttemptLimit != 0 || got.PermanentRejection != 0 {
				t.Fatal("historical pre-assignment cancellation counted as deadline or duplicated", got)
			}
		})
	}
}
