//go:build !postgres

package jobs

import (
	"fmt"
	"time"

	"github.com/mlange-42/ark/ecs"

	"github.com/ziad-hsn/cpra/internal/manifest"
)

func newPulsePostgresJob(cfg *manifest.PulsePostgresConfig, timeout time.Duration, entity ecs.Entity) (Job, error) {
	return nil, fmt.Errorf("postgres check requires building with -tags=postgres")
}
