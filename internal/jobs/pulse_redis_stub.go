//go:build !redis

package jobs

import (
	"fmt"
	"time"

	"github.com/mlange-42/ark/ecs"

	"cpra/internal/loader/schema"
)

func newPulseRedisJob(cfg *schema.PulseRedisConfig, timeout time.Duration, entity ecs.Entity) (Job, error) {
	return nil, fmt.Errorf("redis check requires building with -tags=redis")
}
