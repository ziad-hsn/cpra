package jobs

import (
	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"
)

// Result is a generic structure for returning the outcome of a job.
// It includes the entity it belongs to, any error that occurred, and a flexible payload.
type Result struct {
	Generation uint64
	Endpoint   int
	Color      string
	Err        error
	// Warning describes a successful check that needs attention without recovery.
	Warning string
	Payload map[string]interface{}
	// Type is the job class ("pulse", "intervention", "code"). It is set at
	// job creation so result routing can switch on it without a map lookup.
	// When empty, routing falls back to Payload["type"] for compatibility.
	Type string
	Ent  ecs.Entity
	ID   uuid.UUID
}

// Entity returns the entity associated with the result.
func (r *Result) Entity() ecs.Entity {
	return r.Ent
}

// Error returns the error associated with the result, if any.
func (r *Result) Error() error {
	return r.Err
}
