//go:build !postgres

package jobs

import (
	"fmt"
	"time"

	"github.com/mlange-42/ark/ecs"

	"cpra/internal/loader/schema"
)

func newPulsePostgresJob(cfg *schema.PulsePostgresConfig, timeout time.Duration, entity ecs.Entity) (Job, error) {
	return nil, fmt.Errorf("postgres check requires building with -tags=postgres")
}
