package controller

import (
	"cpra/internal/controller/entities"
	"cpra/internal/logger"
	"runtime/debug"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// RecoverySystem provides system-level error recovery and health monitoring
type RecoverySystem struct {
	LastError   time.Time
	Mapper      *entities.EntityManager
	ErrorCount  int
	MaxErrors   int
	ResetWindow time.Duration
	Logger      logger.Logger
}

func NewRecoverySystem(log logger.Logger, maxErrors int, resetWindow time.Duration) *RecoverySystem {
	return &RecoverySystem{
		MaxErrors:   maxErrors,
		ResetWindow: resetWindow,
		Logger:      log,
	}
}

// SafeSystemUpdate wraps system updates with error recovery
func (r *RecoverySystem) SafeSystemUpdate(systemName string, updateFunc func() error) error {
	defer func() {
		if recovered := recover(); recovered != nil {
			r.ErrorCount++
			r.LastError = time.Now()

			r.Logger.Error("PANIC in system",
				logger.Field{Key: "system", Value: systemName},
				logger.Field{Key: "error", Value: recovered},
				logger.Field{Key: "stack", Value: string(debug.Stack())})

			// Circuit breaker logic
			if r.ErrorCount >= r.MaxErrors {
				r.Logger.Error("System exceeded max errors, entering degraded mode",
					logger.Field{Key: "system", Value: systemName},
					logger.Field{Key: "max_errors", Value: r.MaxErrors})
			}
		}
	}()

	// Reset error count if enough time has passed
	if time.Since(r.LastError) > r.ResetWindow {
		r.ErrorCount = 0
	}

	// Circuit breaker - prevent further damage if too many errors
	if r.ErrorCount >= r.MaxErrors {
		return nil // Skip execution
	}

	return updateFunc()
}

// ValidateEntityHealth checks entity component integrity
func (r *RecoverySystem) ValidateEntityHealth(w *ecs.World, entity ecs.Entity) bool {
	if !w.Alive(entity) {
		return false
	}

	// Check for required components
	if r.Mapper != nil {
		state := r.Mapper.GetMonitorState(entity)
		if state == nil {
			r.Logger.Warn("Entity missing MonitorState component", logger.Field{Key: "entity", Value: entity})
			return false
		}
		if state.Name == "" {
			r.Logger.Warn("Entity missing Name component", logger.Field{Key: "entity", Value: entity})
			return false
		}
	}

	return true
}

// CleanupOrphanedComponents removes components from dead entities
func (r *RecoverySystem) CleanupOrphanedComponents(w *ecs.World) {
	// This would need specific implementation based on component tracking
	// For now, log the cleanup intent
	r.Logger.Info("Cleanup cycle", logger.Field{Key: "active_entities", Value: w.Stats().Entities.Used})
}
