package systems

import (
	"fmt"
	"log/slog"
	"os"
	"sync"

	"cpra/internal/controller/components"

	"github.com/mlange-42/ark/ecs"
)

// StateLogger provides a dedicated logger for tracking ECS state transitions.
// It is designed to provide a clear and detailed audit trail of what is happening to each monitor.
type StateLogger struct {
	logger    *slog.Logger
	mu        sync.Mutex
	debugMode bool
}

// NewStateLogger creates a new StateLogger.
func NewStateLogger(debugMode bool) *StateLogger {
	var h slog.Handler
	if debugMode {
		h = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})
	} else {
		// In non-debug mode, use a no-op handler to discard all logs.
		h = slog.NewJSONHandler(&noopWriter{}, &slog.HandlerOptions{})
	}
	return &StateLogger{
		logger:    slog.New(h),
		debugMode: debugMode,
	}
}

// LogTransition logs a state transition for a monitor.
func (l *StateLogger) LogTransition(entity ecs.Entity, oldState, newState components.MonitorState) {
	if !l.debugMode {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	l.logger.Info("state transition",
		"entity_id", entity.ID(),
		"monitor_name", newState.Name,
		"old_state", formatState(oldState.Flags),
		"new_state", formatState(newState.Flags),
	)
}

// formatState converts the state flags into a human-readable string.
func formatState(flags uint32) string {
	var states []string
	if flags&components.StatePulseNeeded != 0 {
		states = append(states, "PulseNeeded")
	}
	if flags&components.StatePulsePending != 0 {
		states = append(states, "PulsePending")
	}
	if flags&components.StateInterventionNeeded != 0 {
		states = append(states, "InterventionNeeded")
	}
	if flags&components.StateInterventionPending != 0 {
		states = append(states, "InterventionPending")
	}
	if flags&components.StateCodeNeeded != 0 {
		states = append(states, "CodeNeeded")
	}
	if flags&components.StateCodePending != 0 {
		states = append(states, "CodePending")
	}
	if flags&components.StateIncidentOpen != 0 {
		states = append(states, "IncidentOpen")
	}
	if flags&components.StateVerifying != 0 {
		states = append(states, "Verifying")
	}
	if len(states) == 0 {
		return "Idle"
	}
	return fmt.Sprintf("%v", states)
}

// noopWriter is a writer that does nothing.
type noopWriter struct{}

func (w *noopWriter) Write(p []byte) (n int, err error) {
	return len(p), nil
}
