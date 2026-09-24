//go:build !redis

package jobs

import (
	"fmt"
	"time"

	"github.com/mlange-42/ark/ecs"

	"github.com/ziad-hsn/cpra/internal/manifest"
)

func newPulseRedisJob(cfg *manifest.PulseRedisConfig, timeout time.Duration, entity ecs.Entity) (Job, error) {
	return nil, fmt.Errorf("redis check requires building with -tags=redis")
}
