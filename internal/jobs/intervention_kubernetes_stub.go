//go:build !kubernetes

package jobs

import (
	"fmt"

	"github.com/mlange-42/ark/ecs"

	"cpra/internal/loader/schema"
)

func newInterventionKubernetesJob(t *schema.InterventionTargetKubernetes, retries int, entity ecs.Entity) (Job, error) {
	return nil, fmt.Errorf("kubernetes intervention requires building with -tags=kubernetes")
}
