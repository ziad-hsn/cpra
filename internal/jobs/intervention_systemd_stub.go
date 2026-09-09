//go:build !systemd

package jobs

import (
	"fmt"

	"github.com/mlange-42/ark/ecs"

	"cpra/internal/loader/schema"
)

func newInterventionSystemdJob(t *schema.InterventionTargetSystemd, retries int, entity ecs.Entity) (Job, error) {
	return nil, fmt.Errorf("systemd intervention requires building with -tags=systemd")
}
