package controller

import (
	"cpra/internal/controller/entities"
	"cpra/internal/loader/schema"
	"fmt"
	"github.com/mlange-42/ark/ecs"
	"sync"
)

type CPRaWorld struct {
	Mappers *entities.EntityManager
	mu      *sync.Mutex
	World   ecs.World
}

func NewCPRaWorld(manifest *schema.Manifest, world *ecs.World) (*CPRaWorld, error) {
	mu := &sync.Mutex{}
	c := &CPRaWorld{mu: mu, World: ecs.NewWorld()} // Create instance
	c.Mappers = entities.InitializeMappers(world)

	for _, m := range manifest.Monitors {
		err := c.Mappers.CreateEntityFromMonitor(m)
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

func (w *CPRaWorld) IsAlive(e ecs.Entity) bool {
	if w.Mappers.World.Stats().Entities.Total < int(e.ID()) {
		return false
	}
	return w.Mappers.World.Alive(e)

}
