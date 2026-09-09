//go:build !mongo

package jobs

import (
	"fmt"
	"time"

	"github.com/mlange-42/ark/ecs"

	"cpra/internal/loader/schema"
)

func newPulseMongoJob(cfg *schema.PulseMongoConfig, timeout time.Duration, entity ecs.Entity) (Job, error) {
	return nil, fmt.Errorf("mongo check requires building with -tags=mongo")
}
