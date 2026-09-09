//go:build aws

package jobs

import (
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"

	"cpra/internal/loader/schema"
)

// InterventionAWSJob performs a cloud-provider remediation action (EC2 reboot
// as the first concrete operation).
type InterventionAWSJob struct {
	Execution
	EnqueueTime time.Time
	StartTime   time.Time
	Region      string
	Operation   string
	InstanceID  string
	Timeout     time.Duration
	Retries     int
	Entity      ecs.Entity
	ID          uuid.UUID
}

func newInterventionAWSJob(t *schema.InterventionTargetAWS, retries int, entity ecs.Entity) (Job, error) {
	return &InterventionAWSJob{
		ID:         uuid.New(),
		Entity:     entity,
		Region:     t.Region,
		Operation:  t.Operation,
		InstanceID: t.InstanceID,
		Timeout:    t.Timeout,
		Retries:    retries,
	}, nil
}

func (i *InterventionAWSJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(result.Err) }()
	ctx, cancel := operationContext(i.Context(), i.Timeout)
	defer cancel()

	payload := map[string]interface{}{"type": "intervention", "driver": "aws"}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(i.Region))
	if err != nil {
		return Result{ID: i.ID, Ent: i.Entity, Err: fmt.Errorf("failed to load aws config: %w", err), Payload: payload}
	}
	client := ec2.NewFromConfig(cfg, func(o *ec2.Options) { o.RetryMaxAttempts = 1 })

	switch i.Operation {
	case "reboot-instance":
		_, err = client.RebootInstances(ctx, &ec2.RebootInstancesInput{InstanceIds: []string{i.InstanceID}})
	default:
		return Result{ID: i.ID, Ent: i.Entity, Err: fmt.Errorf("unsupported aws operation %q", i.Operation), Payload: payload}
	}
	if err != nil {
		return Result{ID: i.ID, Ent: i.Entity, Err: fmt.Errorf("aws intervention failed: %w", err), Payload: payload}
	}
	return Result{ID: i.ID, Ent: i.Entity, Err: nil, Payload: payload}
}

func (i *InterventionAWSJob) Copy() Job                  { job := *i; return &job }
func (i *InterventionAWSJob) GetEnqueueTime() time.Time  { return i.EnqueueTime }
func (i *InterventionAWSJob) SetEnqueueTime(t time.Time) { i.EnqueueTime = t }
func (i *InterventionAWSJob) GetStartTime() time.Time    { return i.StartTime }
func (i *InterventionAWSJob) SetStartTime(t time.Time)   { i.StartTime = t }
func (i *InterventionAWSJob) IsNil() bool                { return i == nil }
