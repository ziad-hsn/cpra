package controller

import (
	"cpra/internal/controller/entities"
	"cpra/internal/loader/schema"
	"fmt"
	"github.com/mlange-42/ark/ecs"
	//"log"
	//"os"
	"sync"
)

type CPRaWorld struct {
	Mappers *entities.EntityManager
	mu      *sync.Mutex
	World   ecs.World
}

func NewCPRaWorld(manifest *schema.Manifest, world *ecs.World) (*CPRaWorld, error) {
	//mu := &sync.Mutex{}
	//c := &CPRaWorld{mu: mu, World: *world} // Create instance
	mapper := entities.InitializeMappers(world)

	for _, m := range manifest.Monitors {
		err := mapper.CreateEntityFromMonitor(m)
		if err != nil {
			return nil, fmt.Errorf("failed to create entity for monitor %s: %w", m.Name, err)
		}
	}
	//log.Println(world.Stats())
	//os.Exit(0)
	return nil, nil
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
