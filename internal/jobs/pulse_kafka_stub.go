//go:build !kafka

package jobs

import (
	"fmt"
	"time"

	"github.com/mlange-42/ark/ecs"

	"github.com/ziad-hsn/cpra/internal/loader/schema"
)

func newPulseKafkaJob(cfg *schema.PulseKafkaConfig, timeout time.Duration, entity ecs.Entity) (Job, error) {
	return nil, fmt.Errorf("kafka check requires building with -tags=kafka")
}
