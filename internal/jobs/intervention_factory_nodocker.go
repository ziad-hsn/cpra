//go:build nodocker

package jobs

import (
	"fmt"

	"github.com/mlange-42/ark/ecs"

	"cpra/internal/loader/schema"
)

// CreateInterventionJob is a stub implementation used when built with `-tags nodocker`.
func CreateInterventionJob(interventionSchema schema.Intervention, jobID ecs.Entity) (Job, error) {
	if interventionSchema.Action == "docker" {
		return nil, ErrDockerSupportDisabled
	}
	return nil, fmt.Errorf("%w: %s", ErrUnknownInterventionAction, interventionSchema.Action)
}

