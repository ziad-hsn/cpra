//go:build rabbitmq

package jobs

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"
	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/ziad-hsn/cpra/internal/manifest"
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
	tls         checkTLS
}

func newPulseRabbitMQJob(cfg *manifest.PulseRabbitMQConfig, timeout time.Duration, entity ecs.Entity) (Job, error) {
	transport, err := prepareCheckTLS("rabbitmq", cfg, cfg.TLS, cfg.InsecureSkipVerify, cfg.CAFile, cfg.ServerName)
	if err != nil {
		return nil, err
	}
	return &PulseRabbitMQJob{
		tls:     transport,
		ID:      uuid.New(),
		Entity:  entity,
		URL:     cfg.URL,
		Timeout: timeout,
		Retries: cfg.Retries,
		payload: map[string]interface{}{"type": "pulse", "driver": "rabbitmq"},
	}, nil
}

func (p *PulseRabbitMQJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(rabbitmqCheckFailure(result.Err)) }()
	ctx, cancel := operationContext(p.Context(), p.Timeout)
	defer cancel()

	payload := p.payload
	address, err := url.Parse(p.URL)
	if err != nil {
		return Result{ID: p.ID, Ent: p.Entity, Err: fmt.Errorf("invalid RabbitMQ URL"), Payload: payload}
	}
	tlsConfig, err := p.tls.configuration(address.Hostname())
	if err != nil {
		return Result{ID: p.ID, Ent: p.Entity, Err: err, Payload: payload}
	}
	attempts := newPulseAttempts(ctx, p.Retries)
	defer attempts.complete(&result)
	var lastErr error
	for {
		ctx, ok := attempts.next()
		if !ok {
			break
		}
		conn, err := amqp.DialConfig(p.URL, amqp.Config{TLSClientConfig: tlsConfig, Dial: func(network, address string) (net.Conn, error) { return dialBounded(ctx, network, address) }})
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
	}
	return Result{ID: p.ID, Ent: p.Entity, Err: fmt.Errorf("rabbitmq check failed after %d attempt(s): %w", attempts.count, lastErr), Payload: payload}
}

func (p *PulseRabbitMQJob) Copy() Job                  { job := *p; return &job }
func (p *PulseRabbitMQJob) GetEnqueueTime() time.Time  { return p.EnqueueTime }
func (p *PulseRabbitMQJob) SetEnqueueTime(t time.Time) { p.EnqueueTime = t }
func (p *PulseRabbitMQJob) GetStartTime() time.Time    { return p.StartTime }
func (p *PulseRabbitMQJob) SetStartTime(t time.Time)   { p.StartTime = t }
func (p *PulseRabbitMQJob) IsNil() bool                { return p == nil }

func rabbitmqCheckFailure(err error) error {
	if errors.Is(err, amqp.ErrSASL) {
		return err // Unsupported mechanism does not establish a credential rejection.
	}
	if errors.Is(err, amqp.ErrCredentials) {
		return &checkAccessError{code: "authentication_failed", cause: err}
	}
	var provider *amqp.Error
	if errors.As(err, &provider) && provider.Code == amqp.AccessRefused {
		// AMQP ACCESS_REFUSED alone cannot identify a bad password. The
		// driver's credential sentinel above identifies that handshake case.
		return &checkAccessError{code: "authorization_failed", cause: err}
	}
	return err
}
