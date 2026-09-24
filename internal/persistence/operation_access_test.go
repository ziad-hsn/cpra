package persistence

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestOperationContextPreservesOrdinaryReceiptsAndReservationClock(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprint(disk), func(t *testing.T) {
			config := testConfig(t)
			if !disk {
				config.Storage.Mode = "memory"
			}
			s, err := Open(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			at := time.Now().UTC()
			reservation, command := allocatedCommand(t, s, reservationCommand(t, s, "context-operation", at))
			for _, stage := range []string{"reserved", "committed", "completed"} {
				if stage == "committed" {
					if got := submit(t, s, command)[0]; got.Err != nil {
						t.Fatal(got.Err)
					}
				}
				if stage == "completed" {
					original, err := s.Operation(reservation.ID)
					if err != nil {
						t.Fatal(err)
					}
					completeReceipt(t, s, original, true)
				}
				want, err := s.Operation(reservation.ID)
				if err != nil {
					t.Fatal(err)
				}
				before := s.Status().CommittedIndex
				got, err := s.OperationContext(context.Background(), reservation.ID, at.Add(2*time.Second))
				if err != nil || !reflect.DeepEqual(got, want) || got.State != stage || s.Status().CommittedIndex != before {
					t.Fatal("ordinary compatibility", stage, err)
				}
			}
			next, _ := allocatedCommand(t, s, reservationCommand(t, s, "expired-reservation", at))
			expires := s.fsm.image.OperationReservations[next.ID].ExpiresAt
			if _, err := s.OperationContext(context.Background(), next.ID, expires); !errors.Is(err, ErrOperationExpired) {
				t.Fatal("supplied reservation expiry ignored", err)
			}
			if _, err := s.Operation(next.ID); err != nil {
				t.Fatal("context observation mutated reservation", err)
			}
		})
	}
}

func TestOperationContextCancellableOwnerAndHistoryLockWaits(t *testing.T) {
	for _, held := range []string{"store", "fsm", "history"} {
		t.Run(held, func(t *testing.T) {
			s := openCatalogMemory(t)
			head := collectionFill(t, s, 1)
			// A collection ID exercises the ordinary miss that precedes owner-aware
			// fallback, including its terminal-history lookup.
			switch held {
			case "store":
				s.mu.Lock()
			case "fsm":
				s.fsm.mu.Lock()
			case "history":
				s.fsm.history.mu.Lock()
			}
			unlock := func() {
				switch held {
				case "store":
					s.mu.Unlock()
				case "fsm":
					s.fsm.mu.Unlock()
				case "history":
					s.fsm.history.mu.Unlock()
				}
			}
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &collectionOperationHistoryWait{Context: base, entered: make(chan struct{})}
			done := make(chan error, 1)
			go func() { _, err := s.OperationContext(ctx, head.ID, head.ActivityAt); done <- err }()
			select {
			case <-ctx.entered:
			case <-time.After(time.Second):
				unlock()
				t.Fatal("read never reached contended lock")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Error("uncancelable read", err)
				}
			case <-time.After(time.Second):
				unlock()
				t.Fatal("read did not cancel while owner/history locked")
			}
			unlock()
		})
	}
}

func TestOperationContextDetachedHistoryAndPostReadFences(t *testing.T) {
	for _, change := range []string{"progress", "epoch", "failed", "index"} {
		t.Run(change, func(t *testing.T) {
			s := openCatalogMemory(t)
			ctx := context.Background()
			at := time.Now().UTC()
			reservation, command := allocatedCommand(t, s, reservationCommand(t, s, "context-terminal", at))
			committed := submit(t, s, command)[0]
			if committed.Err != nil {
				t.Fatal(committed.Err)
			}
			completeReceipt(t, s, *committed.Operation, true)
			expected, err := s.Operation(reservation.ID)
			if err != nil {
				t.Fatal(err)
			}
			originalEpoch, originalIndex := s.fsm.image.OperationEpoch, s.fsm.image.Index
			history := s.fsm.history
			history.mu.Lock()
			locked := true
			defer func() {
				if locked {
					history.mu.Unlock()
				}
			}()
			base, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			reading := &collectionOperationHistoryWait{Context: base, entered: make(chan struct{})}
			type result struct {
				receipt OperationReceipt
				err     error
			}
			done := make(chan result, 1)
			go func() {
				receipt, err := s.OperationContext(reading, reservation.ID, at.Add(2*time.Second))
				done <- result{receipt, err}
			}()
			select {
			case <-reading.entered:
			case <-base.Done():
				t.Fatal("read did not wait on history")
			}
			progressed := make(chan struct{})
			go func() {
				s.mu.Lock()
				s.fsm.mu.Lock()
				switch change {
				case "progress":
					s.fsm.image.Index++
				case "epoch":
					s.fsm.image.OperationEpoch = uuid.NewString()
				case "index":
					s.fsm.image.Index--
				case "failed":
					s.err = errors.New("injected unavailable storage")
				}
				s.fsm.mu.Unlock()
				s.mu.Unlock()
				close(progressed)
			}()
			select {
			case <-progressed:
			case <-time.After(time.Second):
				history.mu.Unlock()
				locked = false
				cancel()
				<-progressed
				<-done
				t.Fatal("ordinary history read pinned owner locks")
			}
			history.mu.Unlock()
			locked = false
			got := <-done
			want := error(nil)
			switch change {
			case "epoch":
				want = ErrOperationExpired
			case "failed", "index":
				want = ErrHistoryUnavailable
			}
			if !errors.Is(got.err, want) || want == nil && !reflect.DeepEqual(got.receipt, expected) {
				t.Fatal("post-read fence or original fact changed", change, got.err)
			}
			s.mu.Lock()
			s.fsm.mu.Lock()
			s.err = nil
			s.fsm.image.OperationEpoch = originalEpoch
			s.fsm.image.Index = originalIndex
			s.fsm.mu.Unlock()
			s.mu.Unlock()
		})
	}
}
