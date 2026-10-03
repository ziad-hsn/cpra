//go:build externaljobs

package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestWorkerStatusesSessionAvailability(t *testing.T) {
	s, authority, request, policy := workerSessionFixture(t)
	before, err := s.WorkerStatuses(t.Context(), time.Now().UTC())
	if err != nil || len(before) != 1 || before[0].SessionStatus != "no_session" || before[0].CapacityAvailable || !before[0].LastSeenAt.IsZero() {
		t.Fatal("never-polled worker has fabricated observations", before, err)
	}
	response, err := s.CommitWorkerPoll(t.Context(), authority, request)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	current, err := s.WorkerStatuses(t.Context(), at)
	if err != nil || len(current) != 1 || current[0].SessionStatus != "current" || !current[0].CapacityAvailable || current[0].Capacity != request.Capacity || current[0].LastSeenAt.IsZero() {
		t.Fatal("current poll missing", current, err)
	}
	if _, err := s.CommitWorkerPoll(t.Context(), authority, request); err != nil {
		t.Fatal(err)
	}
	repeated, err := s.WorkerStatuses(t.Context(), at)
	if err != nil || !reflect.DeepEqual(repeated, current) {
		t.Fatal("replayed poll renewed the observation", repeated, err)
	}
	expired, err := s.WorkerStatuses(t.Context(), response.SessionExpiresAt)
	if err != nil || expired[0].SessionStatus != "expired" || expired[0].CapacityAvailable || expired[0].Capacity != 0 || expired[0].LastSeenAt != current[0].LastSeenAt {
		t.Fatal("expired session advertised usable capacity", expired, err)
	}
	regressed, err := s.WorkerStatuses(t.Context(), current[0].LastSeenAt.Add(-time.Nanosecond))
	if err != nil || regressed[0].SessionStatus != "clock_regression" || regressed[0].CapacityAvailable {
		t.Fatal("backwards clock accepted a future advertisement", regressed, err)
	}
	current[0].ID = "caller-modified"
	untouched, err := s.WorkerStatuses(t.Context(), at)
	if err != nil || untouched[0].ID != policy.Worker.ID {
		t.Fatal("caller mutated stored identity", untouched, err)
	}
	encoded, _ := json.Marshal(untouched)
	for _, secret := range []string{policy.Worker.TokenSHA256, policy.Worker.CredentialRevision, policy.Worker.GrantRevision, request.ClientSessionID, response.SessionID} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("private authority entered status")
		}
	}
}

func TestWorkerStatusesTerminalActionsAndNoWrites(t *testing.T) {
	for _, category := range []string{"check", "recovery", "notification"} {
		t.Run(category, func(t *testing.T) {
			status := "unknown"
			if category == "check" {
				status = "noData"
			}
			s, authority, _, input := workerResultFixture(t, category, status)
			before, err := s.WorkerStatuses(t.Context(), time.Now().UTC())
			if err != nil || len(before) != 1 || before[0].Active != 1 || before[0].UnknownActions != 0 {
				t.Fatal("outstanding grant missing", before, err)
			}
			if _, err := s.CommitWorkerResult(t.Context(), authority, input); err != nil {
				t.Fatal(err)
			}
			index := s.Status().CommittedIndex
			got, err := s.WorkerStatuses(t.Context(), time.Now().UTC())
			unknown := 1
			if category == "check" {
				unknown = 0
			}
			if err != nil || got[0].Active != 0 || got[0].UnknownActions != unknown || s.Status().CommittedIndex != index {
				t.Fatal("terminal action counted as active or observation wrote state", got, err)
			}
			if category != "check" {
				execution, ok, err := s.WorkerExecution(t.Context(), input.ExecutionID)
				if err != nil || !ok {
					t.Fatal("execution missing", err)
				}
				monitor, ok := s.Get(execution.Intent.MonitorID)
				if !ok {
					t.Fatal("action monitor missing")
				}
				reviewed := mustResult(t, s, reviewCommand(monitor, monitor.Actions[execution.Intent.ActionID], uuid.NewString(), "inconclusive", time.Now().UTC()))
				if action := reviewed.Monitor.Actions[execution.Intent.ActionID]; action.Review == nil || !action.Held() {
					t.Fatal("inconclusive worker review did not retain the action hold")
				}
				afterReview, err := s.WorkerStatuses(t.Context(), time.Now().UTC())
				if err != nil || afterReview[0].UnknownActions != 1 || afterReview[0].Active != 0 {
					t.Fatal("review rewrote original unknown outcome observations", err)
				}
			}
		})
	}
}

func TestWorkerStatusesUnavailableAuthorityAndCancellation(t *testing.T) {
	s, authority, request, policy := workerSessionFixture(t)
	if _, err := s.CommitWorkerPoll(t.Context(), authority, request); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	for _, status := range []string{"previous_owner", "reset_required", "revoked", "credential_expired"} {
		t.Run(status, func(t *testing.T) {
			// Inject each retained authority condition without invoking admin
			// mutation against the live memory owner.
			s.fsm.mu.Lock()
			original := s.fsm.image.WorkerPolicy.Workers[policy.Worker.ID]
			worker := original
			session := s.fsm.image.WorkerSessions.Sessions[worker.UID]
			modified := session
			switch status {
			case "previous_owner":
				modified.OwnerEpoch = "previous-owner"
			case "reset_required":
				worker.ResetRequired = true
			case "revoked":
				worker.Revoked = true
			case "credential_expired":
				worker.ExpiresAt = at
			}
			s.fsm.image.WorkerPolicy.Workers[worker.ID] = worker
			s.fsm.image.WorkerSessions.Sessions[worker.UID] = modified
			s.fsm.mu.Unlock()
			got, err := s.WorkerStatuses(t.Context(), at)
			s.fsm.mu.Lock()
			s.fsm.image.WorkerPolicy.Workers[worker.ID] = original
			s.fsm.image.WorkerSessions.Sessions[worker.UID] = session
			s.fsm.mu.Unlock()
			if err != nil || got[0].SessionStatus != status || got[0].CapacityAvailable {
				t.Fatal(got, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.WorkerStatuses(ctx, at); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored", err)
	}
	if _, err := s.WorkerStatuses(nil, at); !errors.Is(err, ErrWorkerSessionInvalid) {
		t.Fatal("nil context accepted", err)
	}
	s.MarkUnavailable(errors.New("private /storage/path"))
	if _, err := s.WorkerStatuses(t.Context(), at); err == nil {
		t.Fatal("unavailable storage produced observations")
	}
}
