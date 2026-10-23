//go:build kafka

package jobs

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/ziad-hsn/cpra/internal/manifest"
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
	tls         checkTLS
}

func newPulseKafkaJob(cfg *manifest.PulseKafkaConfig, timeout time.Duration, entity ecs.Entity) (Job, error) {
	transport, err := prepareCheckTLS("kafka", cfg, cfg.TLS, cfg.InsecureSkipVerify, cfg.CAFile, cfg.ServerName)
	if err != nil {
		return nil, err
	}
	return &PulseKafkaJob{
		tls:     transport,
		ID:      uuid.New(),
		Entity:  entity,
		Brokers: append([]string(nil), cfg.Brokers...),
		Timeout: timeout,
		Retries: cfg.Retries,
		payload: map[string]interface{}{"type": "pulse", "driver": "kafka"},
	}, nil
}

func (p *PulseKafkaJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(kafkaCheckFailure(result.Err)) }()
	ctx, cancel := operationContext(p.Context(), p.Timeout)
	defer cancel()

	payload := p.payload
	tlsConfig, err := p.tls.configuration("") // Kafka selects each broker's hostname.
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
		options := []kgo.Opt{kgo.SeedBrokers(p.Brokers...), kgo.RequestRetries(0), kgo.DialTimeout(remaining(ctx)), kgo.RequestTimeoutOverhead(remaining(ctx))}
		if tlsConfig != nil {
			options = append(options, kgo.DialTLSConfig(tlsConfig.Clone()))
		}
		client, err := kgo.NewClient(options...)
		if err == nil {
			err = client.Ping(ctx)
			client.Close()
		}
		if err == nil {
			return Result{ID: p.ID, Ent: p.Entity, Err: nil, Payload: payload}
		}
		lastErr = err
	}
	return Result{ID: p.ID, Ent: p.Entity, Err: fmt.Errorf("kafka check failed after %d attempt(s): %w", attempts.count, lastErr), Payload: payload}
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

func kafkaCheckFailure(err error) error {
	var provider *kerr.Error
	if errors.As(err, &provider) {
		switch provider.Code {
		case kerr.SaslAuthenticationFailed.Code:
			return &checkAccessError{code: "authentication_failed", cause: err}
		case kerr.TopicAuthorizationFailed.Code, kerr.GroupAuthorizationFailed.Code,
			kerr.ClusterAuthorizationFailed.Code, kerr.TransactionalIDAuthorizationFailed.Code,
			kerr.DelegationTokenAuthorizationFailed.Code:
			return &checkAccessError{code: "authorization_failed", cause: err}
		}
	}
	return err
}
