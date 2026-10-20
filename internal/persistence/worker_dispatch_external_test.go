//go:build externaljobs

package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestWorkerDispatchCapacityCountsUnknownActions(t *testing.T) {
	for _, status := range []string{"completed", "unknown"} {
		t.Run(status, func(t *testing.T) {
			s, authority, _, result := workerResultFixture(t, "recovery", status)
			record, _, _ := s.WorkerExecution(t.Context(), result.ExecutionID)
			s.fsm.mu.RLock()
			job := s.fsm.image.JobTypes.Records[record.Intent.JobType.JobTypeID].Clone()
			s.fsm.mu.RUnlock()
			for i := 1; i < MaxWorkerExecutionsPerType; i++ {
				in := workerExecutionFixtureType(t, s, job)
				if _, err := s.CommitWorkerExecution(t.Context(), in); err != nil {
					t.Fatal("fill original type", i, err)
				}
			}
			ref := record.Intent.JobType
			if err := s.CheckWorkerExecutionCapacity(t.Context(), ref); !errors.Is(err, ErrWorkerExecutionQuota) {
				t.Fatal("full type was not rejected", err)
			}
			extra := workerExecutionFixtureType(t, s, job)
			if _, err := s.CommitWorkerExecution(t.Context(), extra); !errors.Is(err, ErrWorkerExecutionQuota) {
				t.Fatal("the commit boundary did not independently enforce capacity", err)
			}
			other, _ := workerExecutionFixture(t, s, "check")
			if err := s.CheckWorkerExecutionCapacity(t.Context(), other.JobType); err != nil {
				t.Fatal("one full type blocked another type", err)
			}
			if _, err := s.CommitWorkerResult(t.Context(), authority, result); err != nil {
				t.Fatal(err)
			}
			s.fsm.mu.RLock()
			before, err := json.Marshal(s.fsm.image)
			s.fsm.mu.RUnlock()
			if err != nil {
				t.Fatal(err)
			}
			for range 3 {
				err := s.CheckWorkerExecutionCapacity(t.Context(), ref)
				if status == "unknown" && !errors.Is(err, ErrWorkerExecutionQuota) || status == "completed" && err != nil {
					t.Fatal("capacity confused a confirmed completion with a held unknown action", err)
				}
			}
			s.fsm.mu.RLock()
			after, err := json.Marshal(s.fsm.image)
			s.fsm.mu.RUnlock()
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("capacity reads changed durable records or allocated a check identity", err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if err := s.CheckWorkerExecutionCapacity(ctx, ref); !errors.Is(err, context.Canceled) {
				t.Fatal("capacity read ignored cancellation", err)
			}
		})
	}
}

func TestWorkerDispatchGlobalCapacityStillAllowsActionExpiry(t *testing.T) {
	s := openCatalogMemory(t)
	for range MaxWorkerExecutions / MaxWorkerExecutionsPerType {
		first, job := workerExecutionFixture(t, s, "check")
		for i := range MaxWorkerExecutionsPerType {
			in := first
			if i > 0 {
				in = workerExecutionFixtureType(t, s, job)
			}
			if _, err := s.CommitWorkerExecution(t.Context(), in); err != nil {
				t.Fatal("fill global capacity", err)
			}
		}
	}
	extra, _ := workerExecutionFixture(t, s, "check")
	if err := s.CheckWorkerExecutionCapacity(t.Context(), extra.JobType); !errors.Is(err, ErrWorkerExecutionQuota) {
		t.Fatal("global capacity preflight accepted a new type", err)
	}
	if _, err := s.CommitWorkerExecution(t.Context(), extra); !errors.Is(err, ErrWorkerExecutionQuota) {
		t.Fatal("commit did not enforce global capacity independently", err)
	}
	s.fsm.mu.RLock()
	count := len(s.fsm.image.WorkerExecutions.Records)
	before, err := json.Marshal(s.fsm.image.WorkerExecutions)
	s.fsm.mu.RUnlock()
	if err != nil || count != MaxWorkerExecutions {
		t.Fatal("fixture did not reach the actual global record limit", count, err)
	}
	for _, category := range []string{"recovery", "notification", "inline-notification"} {
		in := workerExpiryFixture(t, s, category)
		original, _ := s.Get(in.MonitorID)
		for range 2 {
			if err := s.ExpireWorkerAction(t.Context(), in); err != nil {
				t.Fatal("full execution store blocked action expiry", category, err)
			}
		}
		m, _ := s.Get(in.MonitorID)
		action := retainedAction(t, s, in.ActionID)
		if action.State != Cancelled || action.Outcome != "worker_admission_expired" ||
			!action.StartedAt.IsZero() || action.ExecutorKind != "" || len(workerExpiryEvents(t, s, in.MonitorID)) != 1 {
			t.Fatal("expiry lost its original disposition or fabricated an invocation", category, action)
		}
		m.Actions[in.ActionID] = original.Actions[in.ActionID]
		m.ActionProjection = original.ActionProjection.clone()
		m.Notifications = original.Notifications.clone()
		m.IncidentDeliveries = original.IncidentDeliveries
		m.incidentNotifications = original.incidentNotifications
		_ = projectionSnapshot(t, s)
		if !reflect.DeepEqual(original, m) {
			t.Fatal("expiry changed health, attempts, cooldown or another action", category)
		}
	}
	s.fsm.mu.RLock()
	after, err := json.Marshal(s.fsm.image.WorkerExecutions)
	s.fsm.mu.RUnlock()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("expiry changed or displaced a retained execution", err)
	}
}

func TestWorkerDispatchLookupKeepsTerminalActions(t *testing.T) {
	for _, category := range []string{"check", "recovery", "notification"} {
		t.Run(category, func(t *testing.T) {
			s, authority, _, result := workerResultFixture(t, category, map[string]string{"check": "success", "recovery": "completed", "notification": "delivered"}[category])
			r, _, _ := s.WorkerExecution(t.Context(), result.ExecutionID)
			before, exists, err := s.FindWorkerExecution(t.Context(), r.Intent.MonitorID, r.Intent.MonitorUID, r.Intent.ActionID)
			if err != nil || !exists || before.ID != result.ExecutionID || before.Terminal {
				t.Fatal("original active execution missing", err)
			}
			if _, err := s.CommitWorkerResult(t.Context(), authority, result); err != nil {
				t.Fatal(err)
			}
			after, exists, err := s.FindWorkerExecution(t.Context(), r.Intent.MonitorID, r.Intent.MonitorUID, r.Intent.ActionID)
			if err != nil || category == "check" && exists || category != "check" && (!exists || !after.Terminal || after.ID != before.ID) {
				t.Fatal("completed action lost its identity or check kept active slot", err)
			}
			status, found, err := s.WorkerDispatchStatus(t.Context(), before.ID)
			if err != nil || !found || !status.Terminal {
				t.Fatal("original receipt disappeared", err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, _, err := s.FindWorkerExecution(ctx, r.Intent.MonitorID, r.Intent.MonitorUID, r.Intent.ActionID); !errors.Is(err, context.Canceled) {
				t.Fatal("lookup ignored cancellation", err)
			}
		})
	}
}

func TestWorkerSLOMergesUnadmittedMissesAndPausesOnce(t *testing.T) {
	s, authority, _, result := workerResultFixture(t, "check", "success")
	if _, err := s.CommitWorkerResult(t.Context(), authority, result); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	s.SLO().Missed("external", at, 2)
	s.SLO().ChangePaused("external", 1, at)
	view := s.SLOView(at.Add(time.Second))
	if len(view.Reports) != 1 {
		t.Fatal("duplicate external reports", view.Reports)
	}
	r := view.Reports[0]
	if r.Expected != 3 || r.Samples != 1 || r.Missed != 2 || r.PausedMonitors != 1 || r.ResultAttainment == nil || *r.ResultAttainment > 1.0/3 || r.QueueAttainment != nil {
		t.Fatal("overlapping or fabricated schedule accounting", r)
	}
}
