package controller

import (
	"cpra/internal/controller/entities"
	"cpra/internal/loader/schema"
	"fmt"
	"github.com/mlange-42/arche/ecs"
	"sync"
)

type CPRaWorld struct {
	Mappers entities.EntityManager
	mu      *sync.Mutex
}

func NewCPRaWorld(manifest *schema.Manifest) (*CPRaWorld, error) {
	mu := &sync.Mutex{}
	c := &CPRaWorld{mu: mu} // Create instance
	w := ecs.NewWorld()
	c.Mappers = entities.InitializeMappers(&w)

	for _, m := range manifest.Monitors {
		err := c.Mappers.CreateEntityFromMonitor(&m)
		if err != nil {
			return nil, fmt.Errorf("failed to create entity for monitor %s: %w", m.Name, err)
		}
	}
	return c, nil
}

func (w *CPRaWorld) SafeAccess(fn func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	fn()
}
