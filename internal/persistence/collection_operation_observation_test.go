package persistence

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestCollectionOperationObservationMetadataFences(t *testing.T) {
	for _, disk := range []bool{false, true} {
		for _, ready := range []bool{false, true} {
			t.Run(fmt.Sprintf("native=%t/ready=%t", disk, ready), func(t *testing.T) {
				var s *Store
				var head CollectionState
				var at time.Time
				if ready {
					s, head = executionPublicationFixture(t, disk, 1)
					head = executionResultViewPublish(t, s, head)
					at = head.ExecutionResult.Summary.FinalizedAt.Add(time.Second)
				} else {
					s, head, _ = preparationCacheFixture(t, disk, 1)
					at = head.Execution.LastAt.Add(time.Second)
				}
				observation, err := s.CollectionOperationObservation(t.Context(), head.ID, head.Actor, at)
				if err != nil || observation.Receipt().ExecutionObservation == nil {
					t.Fatal("capture protected observation", err)
				}
				if _, err := s.CollectionOperationObservation(t.Context(), head.ID, "foreign", at); !errors.Is(err, ErrOperationNotFound) {
					t.Fatal("foreign actor obtained a response fence", err)
				}
				// Final policy admission must not wait for the history lock or disk.
				history := s.History()
				history.mu.Lock()
				ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
				err = observation.Recheck(ctx, at)
				cancel()
				history.mu.Unlock()
				if err != nil {
					t.Fatal("metadata fence acquired history", err)
				}
				for _, fault := range []string{"epoch", "highwater", "index", "history", "owner", "health", "removed"} {
					t.Run(fault, func(t *testing.T) {
						s.fsm.mu.Lock()
						epoch, high, index := s.fsm.image.OperationEpoch, s.fsm.image.OperationHighWater, s.fsm.image.Index
						original := s.fsm.image.Collections[head.ID]
						switch fault {
						case "epoch":
							s.fsm.image.OperationEpoch = uuid.NewString()
						case "highwater":
							s.fsm.image.OperationHighWater = 0
						case "index":
							s.fsm.image.Index = 0
						case "history":
							s.fsm.history = nil
						case "owner":
							changed := original.Clone()
							changed.Actor = "foreign"
							s.fsm.image.Collections[head.ID] = changed
						case "health":
							s.fsm.err = errors.New("injected storage failure")
						case "removed":
							delete(s.fsm.image.Collections, head.ID)
						}
						s.fsm.mu.Unlock()
						err := observation.Recheck(t.Context(), at)
						s.fsm.mu.Lock()
						s.fsm.image.OperationEpoch, s.fsm.image.OperationHighWater, s.fsm.image.Index = epoch, high, index
						s.fsm.image.Collections[head.ID], s.fsm.history, s.fsm.err = original, history, nil
						s.fsm.mu.Unlock()
						if ready && fault == "removed" {
							if err != nil {
								t.Fatal("sealed observation lost original authority after source retirement", err)
							}
						} else if err == nil {
							t.Fatal("stale response survived metadata change", fault)
						}
					})
				}
				if ready {
					expires := head.ExecutionResult.Summary.FinalizedAt.AddDate(0, 0, 30)
					if err := observation.Recheck(t.Context(), expires); !errors.Is(err, ErrOperationExpired) {
						t.Fatal("expired response remained ready", err)
					}
					if err := history.Expire(expires); err != nil {
						t.Fatal(err)
					}
					if err := observation.Recheck(t.Context(), at); !errors.Is(err, ErrOperationExpired) {
						t.Fatal("committed cutoff did not reject stale ready response", err)
					}
				}
			})
		}
	}
}
