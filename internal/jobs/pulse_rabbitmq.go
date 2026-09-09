//go:build rabbitmq

package jobs

import (
	"fmt"
	"net"
	"time"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"
	amqp "github.com/rabbitmq/amqp091-go"

	"cpra/internal/loader/schema"
)

// PulseRabbitMQJob checks a RabbitMQ broker by opening an AMQP connection and
// channel.
type PulseRabbitMQJob struct {
	Execution
	EnqueueTime time.Time
	StartTime   time.Time
	URL         string
	Timeout     time.Duration
	Retries     int
	Entity      ecs.Entity
	ID          uuid.UUID
	payload     map[string]interface{}
}

func newPulseRabbitMQJob(cfg *schema.PulseRabbitMQConfig, timeout time.Duration, entity ecs.Entity) (Job, error) {
	return &PulseRabbitMQJob{
		ID:      uuid.New(),
		Entity:  entity,
		URL:     cfg.URL,
		Timeout: timeout,
		Retries: cfg.Retries,
		payload: map[string]interface{}{"type": "pulse", "driver": "rabbitmq"},
	}, nil
}

func (p *PulseRabbitMQJob) Execute() (result Result) {
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
		conn, err := amqp.DialConfig(p.URL, amqp.Config{Dial: func(network, address string) (net.Conn, error) { return dialBounded(ctx, network, address) }})
		if err == nil {
			var ch *amqp.Channel
			ch, err = conn.Channel()
			if err == nil {
				_ = ch.Close()
			}
			_ = conn.Close()
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
	return Result{ID: p.ID, Ent: p.Entity, Err: fmt.Errorf("rabbitmq check failed after %d attempt(s): %w", attempts, lastErr), Payload: payload}
}

func (p *PulseRabbitMQJob) Copy() Job                  { job := *p; return &job }
func (p *PulseRabbitMQJob) GetEnqueueTime() time.Time  { return p.EnqueueTime }
func (p *PulseRabbitMQJob) SetEnqueueTime(t time.Time) { p.EnqueueTime = t }
func (p *PulseRabbitMQJob) GetStartTime() time.Time    { return p.StartTime }
func (p *PulseRabbitMQJob) SetStartTime(t time.Time)   { p.StartTime = t }
func (p *PulseRabbitMQJob) IsNil() bool                { return p == nil }
