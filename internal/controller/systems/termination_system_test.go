package systems

import (
	"context"
	"testing"
	"time"

	"github.com/mlange-42/ark-tools/app"
	"github.com/mlange-42/ark-tools/resource"
	"github.com/mlange-42/ark/ecs"
)

func newTestApp() *app.App {
	return app.New(64)
}

func TestNewTerminationSystem_NilContext(t *testing.T) {
	t.Parallel()

	s := NewTerminationSystem(nil)
	if s == nil {
		t.Fatal("NewTerminationSystem returned nil")
	}
}

func TestNewTerminationSystem_WithContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewTerminationSystem(ctx)
	if s == nil {
		t.Fatal("NewTerminationSystem returned nil")
	}
}

func TestTerminationSystem_SetContext(t *testing.T) {
	t.Parallel()

	// Start with a non-nil context to initialize the atomic.Value type
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	s := NewTerminationSystem(ctx1)

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	s.SetContext(ctx2)

	// Should not panic - context is stored atomically
}

func TestTerminationSystem_Initialize(t *testing.T) {
	t.Parallel()

	a := newTestApp()
	s := NewTerminationSystem(nil)

	// Initialize should get the termination resource
	s.Initialize(&a.World)

	// Verify it was initialized by checking the termination resource exists
	term := ecs.GetResource[resource.Termination](&a.World)
	if term == nil {
		t.Error("Termination resource should exist after app creation")
	}
}

func TestTerminationSystem_Update_NilContext(t *testing.T) {
	t.Parallel()

	a := newTestApp()
	s := NewTerminationSystem(nil)
	s.Initialize(&a.World)

	// Should not panic or set termination
	s.Update(&a.World)

	term := ecs.GetResource[resource.Termination](&a.World)
	if term.Terminate {
		t.Error("Terminate should be false when context is nil")
	}
}

func TestTerminationSystem_Update_ActiveContext(t *testing.T) {
	t.Parallel()

	a := newTestApp()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewTerminationSystem(ctx)
	s.Initialize(&a.World)

	// Update with active (non-cancelled) context
	s.Update(&a.World)

	term := ecs.GetResource[resource.Termination](&a.World)
	if term.Terminate {
		t.Error("Terminate should be false when context is active")
	}
}

func TestTerminationSystem_Update_CancelledContext(t *testing.T) {
	t.Parallel()

	a := newTestApp()
	ctx, cancel := context.WithCancel(context.Background())
	s := NewTerminationSystem(ctx)
	s.Initialize(&a.World)

	// Cancel the context
	cancel()

	// Update should set termination
	s.Update(&a.World)

	term := ecs.GetResource[resource.Termination](&a.World)
	if !term.Terminate {
		t.Error("Terminate should be true when context is cancelled")
	}
}

func TestTerminationSystem_Update_AlreadyTerminated(t *testing.T) {
	t.Parallel()

	a := newTestApp()
	ctx, cancel := context.WithCancel(context.Background())
	s := NewTerminationSystem(ctx)
	s.Initialize(&a.World)

	// Cancel the context
	cancel()

	// First update sets termination
	s.Update(&a.World)

	// Second update should be idempotent
	s.Update(&a.World)

	// Should still be terminated
	term := ecs.GetResource[resource.Termination](&a.World)
	if !term.Terminate {
		t.Error("Terminate should still be true after multiple updates")
	}
}

func TestTerminationSystem_Finalize(t *testing.T) {
	t.Parallel()

	a := newTestApp()
	s := NewTerminationSystem(nil)
	s.Initialize(&a.World)

	// Should not panic
	s.Finalize(&a.World)
}

func TestTerminationSystem_SetContext_AfterInitialize(t *testing.T) {
	t.Parallel()

	a := newTestApp()
	s := NewTerminationSystem(nil)
	s.Initialize(&a.World)

	// Set context after initialize
	ctx, cancel := context.WithCancel(context.Background())
	s.SetContext(ctx)

	// Update with active context - should not terminate
	s.Update(&a.World)
	term := ecs.GetResource[resource.Termination](&a.World)
	if term.Terminate {
		t.Error("Terminate should be false when context is active")
	}

	// Cancel and update - should terminate
	cancel()
	s.Update(&a.World)
	term = ecs.GetResource[resource.Termination](&a.World)
	if !term.Terminate {
		t.Error("Terminate should be true after context is cancelled")
	}
}

// ============================================================================
// CONCURRENT ACCESS TESTS
// ============================================================================

// TestTerminationSystem_ConcurrentSetContext tests concurrent SetContext calls
func TestTerminationSystem_ConcurrentSetContext(t *testing.T) {
	t.Parallel()

	// Start with an initial context to set the type in atomic.Value
	initialCtx, initialCancel := context.WithCancel(context.Background())
	defer initialCancel()
	s := NewTerminationSystem(initialCtx)

	const goroutines = 10
	const iterations = 100

	done := make(chan bool, goroutines)

	for g := 0; g < goroutines; g++ {
		go func() {
			for i := 0; i < iterations; i++ {
				ctx, cancel := context.WithCancel(context.Background())
				s.SetContext(ctx)
				cancel() // Cancel immediately, we just want to test SetContext
			}
			done <- true
		}()
	}

	for g := 0; g < goroutines; g++ {
		<-done
	}
	// Test passes if no race detected with -race flag
}

// TestTerminationSystem_ConcurrentSetContextAndUpdate tests concurrent SetContext calls
// while Update is called sequentially from a single goroutine (matching ECS architecture).
// Note: Update() is NOT thread-safe by design - ECS systems run sequentially within the
// update loop. Only SetContext() needs to be thread-safe for external callers.
func TestTerminationSystem_ConcurrentSetContextAndUpdate(t *testing.T) {
	t.Parallel()

	a := newTestApp()
	initialCtx, initialCancel := context.WithCancel(context.Background())
	defer initialCancel()
	s := NewTerminationSystem(initialCtx)
	s.Initialize(&a.World)

	const goroutines = 5
	const iterations = 50

	done := make(chan bool, goroutines)

	// Goroutines that call SetContext (thread-safe via atomic.Value)
	for g := 0; g < goroutines; g++ {
		go func() {
			for i := 0; i < iterations; i++ {
				ctx, cancel := context.WithCancel(context.Background())
				s.SetContext(ctx)
				cancel()
			}
			done <- true
		}()
	}

	// Single goroutine calls Update (simulating ECS sequential execution)
	// This runs concurrently with SetContext calls to verify SetContext is safe
	go func() {
		for i := 0; i < iterations*goroutines; i++ {
			s.Update(&a.World)
		}
	}()

	for g := 0; g < goroutines; g++ {
		<-done
	}
	// Test passes if no race detected with -race flag
}

// TestTerminationSystem_DeadlineExceeded tests context.DeadlineExceeded
func TestTerminationSystem_DeadlineExceeded(t *testing.T) {
	t.Parallel()

	a := newTestApp()
	// Create a context with an already-passed deadline
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-1*time.Second))
	defer cancel()

	s := NewTerminationSystem(ctx)
	s.Initialize(&a.World)

	// Update should set termination since deadline is exceeded
	s.Update(&a.World)

	term := ecs.GetResource[resource.Termination](&a.World)
	if !term.Terminate {
		t.Error("Terminate should be true when context deadline is exceeded")
	}
}

// TestTerminationSystem_ContextSwitching tests rapid context switching
func TestTerminationSystem_ContextSwitching(t *testing.T) {
	t.Parallel()

	a := newTestApp()
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	s := NewTerminationSystem(ctx1)
	s.Initialize(&a.World)

	// Update with active context - should not terminate
	s.Update(&a.World)
	term := ecs.GetResource[resource.Termination](&a.World)
	if term.Terminate {
		t.Error("Terminate should be false with active context")
	}

	// Switch to new context
	ctx2, cancel2 := context.WithCancel(context.Background())
	s.SetContext(ctx2)

	// Update with new active context - should not terminate
	s.Update(&a.World)
	term = ecs.GetResource[resource.Termination](&a.World)
	if term.Terminate {
		t.Error("Terminate should be false after context switch")
	}

	// Cancel the second context
	cancel2()
	s.Update(&a.World)
	term = ecs.GetResource[resource.Termination](&a.World)
	if !term.Terminate {
		t.Error("Terminate should be true after second context cancelled")
	}
}

// TestTerminationSystem_MultipleUpdatesAfterCancel tests multiple updates after cancellation
func TestTerminationSystem_MultipleUpdatesAfterCancel(t *testing.T) {
	t.Parallel()

	a := newTestApp()
	ctx, cancel := context.WithCancel(context.Background())
	s := NewTerminationSystem(ctx)
	s.Initialize(&a.World)

	cancel()

	// Multiple updates should all be safe
	for i := 0; i < 100; i++ {
		s.Update(&a.World)
	}

	term := ecs.GetResource[resource.Termination](&a.World)
	if !term.Terminate {
		t.Error("Terminate should be true")
	}
}

// ============================================================================
// BENCHMARKS
// ============================================================================

func BenchmarkTerminationSystem_Update_ActiveContext(b *testing.B) {
	a := newTestApp()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewTerminationSystem(ctx)
	s.Initialize(&a.World)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Update(&a.World)
	}
}

func BenchmarkTerminationSystem_SetContext(b *testing.B) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewTerminationSystem(ctx)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		newCtx, newCancel := context.WithCancel(context.Background())
		s.SetContext(newCtx)
		newCancel()
	}
}
