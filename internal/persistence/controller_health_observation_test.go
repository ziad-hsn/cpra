package persistence

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestControllerHealthObservationDoesNotWaitForFSMWriter(t *testing.T) {
	s := openCatalogMemory(t)
	m, guard := managedControlMonitor(t, s, "observation-contention")
	s.fsm.mu.Lock()
	defer s.fsm.mu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := s.ControllerHealthContext(ctx); err != nil {
		t.Fatal("routine health observation treated a progressing FSM writer as failed storage", err)
	}
	if status, err := s.StatusContext(ctx); err != nil || !status.Ready {
		t.Fatal("routine readiness waited on the FSM writer", status, err)
	}
	// A health observation is not an execution grant. Authoritative check/start
	// admission continues to wait for committed state and honor its deadline.
	if err := s.CheckCheckAdmissionContext(ctx, m.ID, guard, m.ControlRevision, time.Now().UTC()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("authoritative admission bypassed the FSM write lock", err)
	}
}
