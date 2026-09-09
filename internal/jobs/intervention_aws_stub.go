//go:build !aws

package jobs

import (
	"fmt"

	"github.com/mlange-42/ark/ecs"

	"cpra/internal/loader/schema"
)

func newInterventionAWSJob(t *schema.InterventionTargetAWS, retries int, entity ecs.Entity) (Job, error) {
	return nil, fmt.Errorf("aws intervention requires building with -tags=aws")
}
