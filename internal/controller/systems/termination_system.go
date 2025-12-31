package systems

import (
	"context"
	"sync/atomic"

	"github.com/mlange-42/ark-tools/resource"
	"github.com/mlange-42/ark/ecs"
)

// TerminationSystem monitors a context and sets the Termination resource when cancelled.
// This allows thread-safe termination signaling by checking the context from within
// the ECS run loop, avoiding races with external writers to the Termination resource.
type TerminationSystem struct {
	termination *resource.Termination
	ctx         atomic.Value // Stores context.Context atomically
	terminated  atomic.Bool  // Track if we've already set termination
}

// NewTerminationSystem creates a new termination system that monitors the given context.
func NewTerminationSystem(ctx context.Context) *TerminationSystem {
	s := &TerminationSystem{}
	if ctx != nil {
		s.ctx.Store(ctx)
	}
	return s
}

// SetContext updates the context to monitor. Thread-safe.
func (s *TerminationSystem) SetContext(ctx context.Context) {
	s.ctx.Store(ctx)
}

// Initialize sets up the system's resource pointers.
func (s *TerminationSystem) Initialize(w *ecs.World) {
	s.termination = ecs.GetResource[resource.Termination](w)
}

// Update checks if the context is cancelled and sets termination if so.
func (s *TerminationSystem) Update(w *ecs.World) {
	// Skip if already terminated
	if s.terminated.Load() {
		return
	}

	ctxVal := s.ctx.Load()
	if ctxVal == nil {
		return
	}

	ctx, ok := ctxVal.(context.Context)
	if !ok || ctx == nil {
		return
	}

	if ctx.Err() != nil {
		s.termination.Terminate = true
		s.terminated.Store(true)
	}
}

// Finalize performs cleanup (no-op for this system).
func (s *TerminationSystem) Finalize(w *ecs.World) {}
