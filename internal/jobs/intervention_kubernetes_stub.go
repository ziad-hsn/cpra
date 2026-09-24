//go:build !kubernetes

package jobs

import (
	"fmt"

	"github.com/mlange-42/ark/ecs"

	"github.com/ziad-hsn/cpra/internal/manifest"
)

func newInterventionKubernetesJob(t *manifest.InterventionTargetKubernetes, retries int, entity ecs.Entity) (Job, error) {
	return nil, fmt.Errorf("kubernetes intervention requires building with -tags=kubernetes")
}
