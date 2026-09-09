//go:build !mysql

package jobs

import (
	"fmt"
	"time"

	"github.com/mlange-42/ark/ecs"

	"cpra/internal/loader/schema"
)

func newPulseMySQLJob(cfg *schema.PulseMySQLConfig, timeout time.Duration, entity ecs.Entity) (Job, error) {
	return nil, fmt.Errorf("mysql check requires building with -tags=mysql")
}
