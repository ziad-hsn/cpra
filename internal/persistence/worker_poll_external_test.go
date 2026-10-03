//go:build externaljobs

package persistence

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
)

func TestWorkerPollWaitDeadlineAndReplay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, authority, request, _, _ := workerPollFixture(t, "check")
		request.WaitSeconds = 25
		before := s.Status().CommittedIndex
		started := time.Now()
		if err := s.WaitWorkerPoll(t.Context(), authority, request); err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(started); elapsed != 25*time.Second {
			t.Fatal("empty polling did not honor requested wait", elapsed)
		}
		if s.Status().CommittedIndex != before || s.fsm.image.WorkerSessions != nil {
			t.Fatal("waiting committed or renewed a session")
		}
		response, err := s.CommitWorkerPoll(t.Context(), authority, request)
		if err != nil || len(response.Offers) != 0 {
			t.Fatal("empty response", err)
		}
		original := s.fsm.image.WorkerSessions.clone()
		started = time.Now()
		if err := s.WaitWorkerPoll(t.Context(), authority, request); err != nil || time.Since(started) != 0 {
			t.Fatal("lost reply waited again", err)
		}
		if !reflect.DeepEqual(original, s.fsm.image.WorkerSessions) {
			t.Fatal("waiting changed the committed reply")
		}
		changed := request.clone()
		changed.WaitSeconds--
		if err := s.WaitWorkerPoll(t.Context(), authority, changed); !errors.Is(err, ErrWorkerSessionConflict) {
			t.Fatal("changed replay was not rejected", err)
		}
	})
}

func TestWorkerPollWaitArrivingWork(t *testing.T) {
	for _, category := range []string{"check", "recovery", "notification"} {
		t.Run(category, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, authority, request, intent, _ := workerPollFixture(t, category)
				request.WaitSeconds = 25
				before := s.Status().CommittedIndex
				finished := make(chan error, 1)
				go func() { finished <- s.WaitWorkerPoll(t.Context(), authority, request) }()
				synctest.Wait()
				select {
				case err := <-finished:
					t.Fatal("poll returned before work arrived", err)
				default:
				}
				if s.Status().CommittedIndex != before {
					t.Fatal("waiting wrote to Raft")
				}
				if _, err := s.CommitWorkerExecution(t.Context(), intent); err != nil {
					t.Fatal(err)
				}
				if err := <-finished; err != nil {
					t.Fatal(err)
				}
				if s.fsm.image.WorkerSessions != nil {
					t.Fatal("availability read reserved work")
				}
				response, err := s.CommitWorkerPoll(t.Context(), authority, request)
				if err != nil || len(response.Offers) != 1 || response.Offers[0].ExecutionID != intent.ID {
					t.Fatal("arriving work was not offered", err)
				}
			})
		})
	}
}

func TestWorkerPollWaitCancellationAndShutdown(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline", "shutdown", "storage-failure"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, authority, request, _, _ := workerPollFixture(t, "check")
				request.WaitSeconds = 25
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if mode == "deadline" {
					var stop context.CancelFunc
					ctx, stop = context.WithTimeout(ctx, 250*time.Millisecond)
					defer stop()
				}
				finished := make(chan error, 1)
				go func() { finished <- s.WaitWorkerPoll(ctx, authority, request) }()
				synctest.Wait()
				switch mode {
				case "cancel":
					cancel()
				case "shutdown":
					if err := s.Close(); err != nil {
						t.Fatal(err)
					}
				case "storage-failure":
					s.MarkUnavailable(errors.New("storage failed"))
				}
				if err := <-finished; err == nil || mode == "cancel" && !errors.Is(err, context.Canceled) || mode == "deadline" && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("wait did not terminate with its cause", err)
				}
				if s.fsm.image.WorkerSessions != nil {
					t.Fatal("interrupted wait committed a response")
				}
			})
		})
	}
}

func TestWorkerPollWaitCredentialExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, _, request, _, worker := workerPollFixture(t, "check")
		s.administrative = true
		worker.ExpiresAt = time.Now().Add(time.Second)
		worker.CredentialRevision = uuid.NewString()
		policy := commitWorker(t, s, workerCommand(t, s, "upsert", worker))
		s.administrative = false
		if wait := time.Until(policy.UpdatedAt); wait > 0 {
			time.Sleep(wait + time.Millisecond)
		}
		authority, err := s.AuthenticateWorker(t.Context(), worker.TokenSHA256, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		request.WaitSeconds = 25
		started := time.Now()
		if err := s.WaitWorkerPoll(t.Context(), authority, request); !errors.Is(err, ErrWorkerAuthorityDenied) {
			t.Fatal("expired authority kept polling", err)
		}
		if time.Since(started) > time.Second+workerPollInterval || s.fsm.image.WorkerSessions != nil {
			t.Fatal("expiry did not promptly terminate without a write")
		}
	})
}

func TestWorkerPollWaitRechecksAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, authority, request, intent, _ := workerOfferFixture(t, "check")
		request.WaitSeconds = 25
		if err := s.WaitWorkerPoll(t.Context(), authority, request); err != nil {
			t.Fatal(err)
		}
		// Another valid polling request may claim work after the read. Waiting
		// creates no grant; only the committed offer owns it.
		first, err := s.CommitWorkerPoll(t.Context(), authority, request)
		if err != nil || len(first.Offers) != 1 || first.Offers[0].ExecutionID != intent.ID {
			t.Fatal(err)
		}
		request.SessionID, request.PollSequence = first.SessionID, 2
		ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
		defer cancel()
		if err := s.WaitWorkerPoll(ctx, authority, request); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("already offered work incorrectly woke the next poll", err)
		}
		stored, ok, err := s.WorkerExecution(t.Context(), intent.ID)
		if err != nil || !ok || stored.Lifecycle.Phase != "offered" || stored.Lifecycle.LeaseID != first.Offers[0].LeaseID {
			t.Fatal("waiting changed an existing lease", err)
		}
	})
}
