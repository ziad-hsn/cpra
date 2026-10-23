//go:build externaljobs

package persistence

import (
	"testing"
	"time"
)

func TestWorkerNotificationBudgetBeginsAtFirstGrant(t *testing.T) {
	s, authority, request, in, _ := workerPollFixture(t, "notification", WorkerNotificationConditionFormatVersion)
	if in.ExecutionBudget <= 0 || !in.Deadline.IsZero() {
		t.Fatal("new notification fixture did not capture a start-based budget")
	}
	if _, err := s.CommitWorkerExecution(t.Context(), in); err != nil {
		t.Fatal(err)
	}
	record, _, err := s.WorkerExecution(t.Context(), in.ID)
	if err != nil || !record.ExecutionDeadline().IsZero() || workerFinalizationReason(record, record.OwnerEpoch, in.Scheduled.Add(48*time.Hour)) != "" {
		t.Fatal("queued condition consumed or expired its execution budget", err)
	}
	response, err := s.CommitWorkerPoll(t.Context(), authority, request)
	if err != nil || len(response.Offers) != 1 {
		t.Fatal("offer", err, response)
	}
	record, _, _ = s.WorkerExecution(t.Context(), in.ID)
	upper := record.Lifecycle.OfferDeadline.Add(in.ExecutionBudget)
	if !response.Offers[0].Deadline.Equal(upper) || record.Lifecycle.OfferDeadline.Sub(record.Lifecycle.OfferedAt) > WorkerOfferLifetime {
		t.Fatal("offer conflated its lifetime with the execution budget")
	}
	start := workerStartFromOffer(response)
	at := record.Lifecycle.OfferedAt.Add(time.Second)
	granted := workerStartAt(t, s, authority, start, at)
	if granted.Err != nil || granted.WorkerStart == nil || granted.WorkerStart.Disposition != "granted" || !granted.WorkerStart.Deadline.Equal(at.Add(in.ExecutionBudget)) {
		t.Fatal("first grant deadline", granted.Err, granted.WorkerStart)
	}
	// A lost first reply never yields another executable grant or a new deadline.
	repeated := workerStartAt(t, s, authority, start, at.Add(time.Second))
	if repeated.Err != nil || repeated.WorkerStart == nil || repeated.WorkerStart.Disposition != "started" || !repeated.WorkerStart.Deadline.Equal(granted.WorkerStart.Deadline) {
		t.Fatal("repeated start renewed permission", repeated.Err, repeated.WorkerStart)
	}
	record, _, _ = s.WorkerExecution(t.Context(), in.ID)
	if !record.ExecutionDeadline().Equal(granted.WorkerStart.Deadline) || record.Intent.Deadline != in.Deadline || record.Digest != workerExecutionDigest(in) {
		t.Fatal("grant modified the sealed intent or its deadline")
	}
	if err := validateWorkerExecutionImage(s.fsm.image); err != nil {
		t.Fatal("snapshot validation", err)
	}
}

func TestWorkerNotificationBudgetIsAuthenticatedAndNotificationOnly(t *testing.T) {
	s, _, _, in, _ := workerPollFixture(t, "notification", WorkerNotificationConditionFormatVersion)
	before := workerExecutionDigest(in)
	in.ExecutionBudget += time.Second
	if before == workerExecutionDigest(in) || in.minimumFormat() != WorkerNotificationConditionFormatVersion {
		t.Fatal("new timing did not enter the versioned identity")
	}
	if got := workerExecutionSubmitAt(t, s, in, s.executorSession, time.Now().UTC()); got.Err == nil {
		t.Fatal("changed captured budget was admitted")
	}
	in.Category = "check"
	if in.ValidTiming() {
		t.Fatal("start-based budget weakened check cadence")
	}
}
