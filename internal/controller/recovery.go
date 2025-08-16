package controller

import (
	"cpra/internal/controller/entities"
	"log"
	"runtime/debug"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// RecoverySystem provides system-level error recovery and health monitoring
type RecoverySystem struct {
	ErrorCount   int
	LastError    time.Time
	MaxErrors    int
	ResetWindow  time.Duration
	Mapper       *entities.EntityManager
}

func NewRecoverySystem(maxErrors int, resetWindow time.Duration) *RecoverySystem {
	return &RecoverySystem{
		MaxErrors:   maxErrors,
		ResetWindow: resetWindow,
	}
}

// SafeSystemUpdate wraps system updates with error recovery
func (r *RecoverySystem) SafeSystemUpdate(systemName string, updateFunc func() error) error {
	defer func() {
		if recovered := recover(); recovered != nil {
			r.ErrorCount++
			r.LastError = time.Now()
			
			log.Printf("PANIC in system %s: %v", systemName, recovered)
			log.Printf("Stack trace: %s", debug.Stack())
			
			// Circuit breaker logic
			if r.ErrorCount >= r.MaxErrors {
				log.Printf("System %s exceeded max errors (%d), entering degraded mode", 
					systemName, r.MaxErrors)
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
		if r.Mapper.Name.Get(entity) == nil {
			log.Printf("Entity %v missing Name component", entity)
			return false
		}
	}

	return true
}

// CleanupOrphanedComponents removes components from dead entities
func (r *RecoverySystem) CleanupOrphanedComponents(w *ecs.World) {
	// This would need specific implementation based on component tracking
	// For now, log the cleanup intent
	log.Printf("Cleanup cycle: %d entities active", w.Stats().Entities.Used)
}