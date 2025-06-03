package controller

import (
	"cpra/internal/controller/entities"
	"cpra/internal/loader/schema"
	"fmt"
	"github.com/mlange-42/arche/ecs"
)

type CPRaWorld struct {
	Mappers entities.EntityManager
}

func NewCPRaWorld(manifest *schema.Manifest) (*CPRaWorld, error) {
	c := &CPRaWorld{} // Create instance
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
