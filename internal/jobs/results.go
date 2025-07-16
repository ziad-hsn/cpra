package jobs

import (
	"github.com/google/uuid"
	"github.com/mlange-42/arche/ecs"
)

type Result interface {
	Entity() ecs.Entity
	Error() error
}

type PulseResults struct {
	ID      uuid.UUID
	Ent     ecs.Entity
	latency int // optional delete later if not needed
	Err     error
}

func (p PulseResults) Entity() ecs.Entity {
	return p.Ent
}

func (p PulseResults) Error() error { return p.Err }

type InterventionResults struct {
	ID      uuid.UUID
	Ent     ecs.Entity
	latency int // optional delete later if not needed
	Err     error
}

func (p InterventionResults) Entity() ecs.Entity {
	return p.Ent
}

func (p InterventionResults) Error() error { return p.Err }

type CodeResults struct {
	ID  uuid.UUID
	Ent ecs.Entity
	Err error
}

func (c CodeResults) Entity() ecs.Entity {
	return c.Ent
}

func (c CodeResults) Error() error { return c.Err }
