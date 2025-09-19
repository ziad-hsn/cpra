package jobs

import (
	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"
)

// Result is a generic structure for returning the outcome of a job.
// It includes the entity it belongs to, any error that occurred, and a flexible payload.
type Result struct {
	ID      uuid.UUID
	Ent     ecs.Entity
	Err     error
	Payload map[string]interface{}
}

// Entity returns the entity associated with the result.
func (r *Result) Entity() ecs.Entity {
	return r.Ent
}

// Error returns the error associated with the result, if any.
func (r *Result) Error() error {
	return r.Err
}