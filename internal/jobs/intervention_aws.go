//go:build aws

package jobs

import (
	"fmt"
	"net/http"
	"time"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"

	"github.com/ziad-hsn/cpra/internal/manifest"
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

func newInterventionAWSJob(t *manifest.InterventionTargetAWS, retries int, entity ecs.Entity) (Job, error) {
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
		return Result{ID: i.ID, Ent: i.Entity, Err: noEffect(fmt.Errorf("failed to load aws config: %w", err), false), Payload: payload}
	}
	builder, ok := cfg.HTTPClient.(*awshttp.BuildableClient)
	if cfg.HTTPClient == nil {
		builder, ok = awshttp.NewBuildableClient(), true
	}
	if !ok {
		return Result{ID: i.ID, Ent: i.Entity, Err: noEffect(fmt.Errorf("unsupported AWS HTTP transport"), false), Payload: payload}
	}
	transport := builder.GetTransport()
	transport.DisableKeepAlives = true
	transport.DialContext = effectDial(transport.DialContext)
	httpClient := &http.Client{Timeout: builder.GetTimeout(), Transport: effectTransport{transport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer httpClient.CloseIdleConnections()
	client := ec2.NewFromConfig(cfg, func(o *ec2.Options) {
		o.RetryMaxAttempts = 1
		o.HTTPClient = httpClient
	})

	switch i.Operation {
	case "reboot-instance":
		_, err = client.RebootInstances(ctx, &ec2.RebootInstancesInput{InstanceIds: []string{i.InstanceID}})
	default:
		return Result{ID: i.ID, Ent: i.Entity, Err: noEffect(fmt.Errorf("unsupported aws operation %q", i.Operation), false), Payload: payload}
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
