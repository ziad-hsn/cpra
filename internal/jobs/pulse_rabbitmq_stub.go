//go:build !rabbitmq

package jobs

import (
	"fmt"
	"time"

	"github.com/mlange-42/ark/ecs"

	"cpra/internal/loader/schema"
)

func newPulseRabbitMQJob(cfg *schema.PulseRabbitMQConfig, timeout time.Duration, entity ecs.Entity) (Job, error) {
	return nil, fmt.Errorf("rabbitmq check requires building with -tags=rabbitmq")
}
