//go:build externaljobs

package persistence

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func committedWorkerAction(t *testing.T, category, status string) (image, Action, WorkerExecutionRecord) {
	t.Helper()
	s, authority, _, input := workerResultFixture(t, category, status)
	if _, err := s.CommitWorkerResult(t.Context(), authority, input); err != nil {
		t.Fatal(err)
	}
	execution, ok, err := s.WorkerExecution(t.Context(), input.ExecutionID)
	if err != nil || !ok {
		t.Fatal("execution", err)
	}
	row, ok, err := s.ActionContext(t.Context(), execution.Intent.ActionID)
	if err != nil || !ok {
		t.Fatal("action", err)
	}
	s.fsm.mu.RLock()
	i := image{Version: s.fsm.image.Version, LocalExecutorSession: s.fsm.image.LocalExecutorSession}
	i.WorkerExecutions = s.fsm.image.WorkerExecutions.clone()
	s.fsm.mu.RUnlock()
	return i, row.Action, execution
}

func TestWorkerActionValidationRetainsReceiptRequirements(t *testing.T) {
	for _, tc := range []struct {
		category, status string
		retiredAllowed   bool
	}{
		{"recovery", "accepted", true},
		{"recovery", "completed", true},
		{"recovery", "rejected", true},
		{"recovery", "unknown", false},
		{"notification", "delivered", false},
	} {
		t.Run(tc.category+"/"+tc.status, func(t *testing.T) {
			i, a, execution := committedWorkerAction(t, tc.category, tc.status)
			for _, present := range []bool{true, false} {
				candidate := i
				candidate.WorkerExecutions = i.WorkerExecutions.clone()
				if !present {
					delete(candidate.WorkerExecutions.Records, execution.Intent.ID)
				}
				validation, err := newWorkerActionValidation(candidate)
				if err != nil {
					t.Fatal(err)
				}
				for _, archived := range []bool{false, true} {
					err := validateActionEvidence(candidate, a, archived, validation)
					want := present || archived && tc.retiredAllowed
					if (err == nil) != want || err != nil && !errors.Is(err, ErrExecutorUnfenced) {
						t.Fatalf("present=%t archived=%t: %v; want valid=%t", present, archived, err, want)
					}
				}
			}
		})
	}
}

func TestWorkerActionValidationRejectsContradictoryRetainedReceipt(t *testing.T) {
	i, a, execution := committedWorkerAction(t, "recovery", "completed")
	for _, tc := range []struct {
		name   string
		change func(*WorkerExecutionRecord)
	}{
		{"incarnation", func(r *WorkerExecutionRecord) { r.Intent.MonitorUID = "different-incarnation" }},
		{"configuration", func(r *WorkerExecutionRecord) { r.Intent.MonitorRevision = "different-revision" }},
		{"grant", func(r *WorkerExecutionRecord) { r.Lifecycle.GrantID = "different-grant" }},
		{"start", func(r *WorkerExecutionRecord) { r.Lifecycle.StartedAt = r.Lifecycle.StartedAt.Add(time.Nanosecond) }},
		{"phase", func(r *WorkerExecutionRecord) { r.Lifecycle.Phase = "started" }},
		{"outcome", func(r *WorkerExecutionRecord) { r.Result.Input.Status = "rejected" }},
		{"missing-lifecycle", func(r *WorkerExecutionRecord) { r.Lifecycle = nil }},
		{"missing-result", func(r *WorkerExecutionRecord) { r.Result = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := i
			candidate.WorkerExecutions = i.WorkerExecutions.clone()
			r := execution.Clone()
			tc.change(&r)
			candidate.WorkerExecutions.Records[r.Intent.ID] = r
			// A deliberately stale runtime index must not hide the retained receipt.
			candidate.WorkerExecutions.actions = map[string]string{a.ID: "absent-execution"}
			before := candidate.WorkerExecutions.clone()
			before.actions = map[string]string{a.ID: "absent-execution"}
			validation, err := newWorkerActionValidation(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateActionEvidence(candidate, a, true, validation); !errors.Is(err, ErrExecutorUnfenced) {
				t.Fatal("contradictory retained receipt used retired-receipt fallback", err)
			}
			if !reflect.DeepEqual(before, candidate.WorkerExecutions) {
				t.Fatal("validation changed the frozen records or runtime indexes")
			}
		})
	}
}

func TestWorkerActionValidationRejectsDuplicateAndExcessRecords(t *testing.T) {
	i, a, execution := committedWorkerAction(t, "recovery", "completed")
	duplicate := execution.Clone()
	duplicate.Intent.ID = "different-execution"
	duplicate.Result.Input.Status = "rejected"
	i.WorkerExecutions.Records[duplicate.Intent.ID] = duplicate
	// Even an apparently valid derived index must not choose one conflicting record.
	i.WorkerExecutions.actions = map[string]string{a.ID: execution.Intent.ID}
	if _, err := newWorkerActionValidation(i); !errors.Is(err, ErrWorkerExecutionConflict) {
		t.Fatal("duplicate action identity accepted", err)
	}
	i.WorkerExecutions.Records = make(map[string]WorkerExecutionRecord, MaxWorkerExecutions+1)
	for n := range MaxWorkerExecutions + 1 {
		i.WorkerExecutions.Records[fmt.Sprint(n)] = WorkerExecutionRecord{}
	}
	if _, err := newWorkerActionValidation(i); !errors.Is(err, ErrWorkerExecutionQuota) {
		t.Fatal("oversized execution inventory accepted", err)
	}
}

// This isolates the forward action/execution join from ledger decoding and I/O.
// The retained-snapshot process check measures the complete restoration path.
func BenchmarkWorkerActionValidation(b *testing.B) {
	for _, records := range []int{0, 500, 4000} {
		for _, actions := range []int{500, 5000} {
			b.Run(fmt.Sprintf("actions=%d/receipts=%d", actions, records), func(b *testing.B) {
				i := image{Version: LatestFormatVersion}
				i.WorkerExecutions = &workerExecutionImage{Records: make(map[string]WorkerExecutionRecord)}
				at := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
				rows := make([]Action, actions)
				for n := range max(records, actions) {
					id := fmt.Sprintf("action-%08d", n)
					a := Action{ID: id, Kind: "intervention", CatalogUID: "incarnation", Revision: "revision", State: Succeeded, Outcome: "completed", ExecutorKind: "worker", ExecutorSession: "grant", StartedAt: at, FinishedAt: at.Add(time.Second)}
					if n < actions {
						rows[n] = a
					}
					if n < records {
						i.WorkerExecutions.Records[id] = WorkerExecutionRecord{
							Intent:    WorkerExecutionIntent{ActionID: id, MonitorUID: a.CatalogUID, MonitorRevision: a.Revision},
							Lifecycle: &WorkerExecutionLifecycle{GrantID: a.ExecutorSession, StartedAt: at, Phase: "finished"},
							Result:    &WorkerResultRecord{Input: WorkerResultInput{Status: "completed"}},
						}
					}
				}
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					validation, err := newWorkerActionValidation(i)
					if err != nil {
						b.Fatal(err)
					}
					for _, a := range rows {
						if err := validateActionEvidence(i, a, true, validation); err != nil {
							b.Fatal(err)
						}
					}
				}
			})
		}
	}
}
