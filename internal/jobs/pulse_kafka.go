//go:build kafka

package jobs

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"
	"github.com/twmb/franz-go/pkg/kgo"

	"cpra/internal/loader/schema"
)

// PulseKafkaJob checks a Kafka cluster by connecting to seed brokers and
// issuing a metadata ping.
type PulseKafkaJob struct {
	Execution
	EnqueueTime time.Time
	StartTime   time.Time
	Brokers     []string
	Timeout     time.Duration
	Retries     int
	Entity      ecs.Entity
	ID          uuid.UUID
	payload     map[string]interface{}
}

func newPulseKafkaJob(cfg *schema.PulseKafkaConfig, timeout time.Duration, entity ecs.Entity) (Job, error) {
	return &PulseKafkaJob{
		ID:      uuid.New(),
		Entity:  entity,
		Brokers: append([]string(nil), cfg.Brokers...),
		Timeout: timeout,
		Retries: cfg.Retries,
		payload: map[string]interface{}{"type": "pulse", "driver": "kafka"},
	}, nil
}

func (p *PulseKafkaJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(result.Err) }()
	ctx, cancel := operationContext(p.Context(), p.Timeout)
	defer cancel()

	payload := p.payload
	attempts := p.Retries + 1
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		client, err := kgo.NewClient(kgo.SeedBrokers(p.Brokers...), kgo.RequestRetries(0), kgo.DialTimeout(remaining(ctx)), kgo.RequestTimeoutOverhead(remaining(ctx)))
		if err == nil {
			err = client.Ping(ctx)
			client.Close()
		}
		if err == nil {
			return Result{ID: p.ID, Ent: p.Entity, Err: nil, Payload: payload}
		}
		lastErr = err
		if attempt < attempts-1 {
			if !retryDelay(ctx) {
				break
			}
		}
	}
	return Result{ID: p.ID, Ent: p.Entity, Err: fmt.Errorf("kafka check failed after %d attempt(s): %w", attempts, lastErr), Payload: payload}
}

func (p *PulseKafkaJob) Copy() Job {
	job := *p
	job.Brokers = append([]string(nil), p.Brokers...)
	return &job
}
func (p *PulseKafkaJob) GetEnqueueTime() time.Time  { return p.EnqueueTime }
func (p *PulseKafkaJob) SetEnqueueTime(t time.Time) { p.EnqueueTime = t }
func (p *PulseKafkaJob) GetStartTime() time.Time    { return p.StartTime }
func (p *PulseKafkaJob) SetStartTime(t time.Time)   { p.StartTime = t }
func (p *PulseKafkaJob) IsNil() bool                { return p == nil }
