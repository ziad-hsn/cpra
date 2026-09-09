//go:build redis

package jobs

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"
	"github.com/redis/go-redis/v9"

	"cpra/internal/loader/schema"
)

// PulseRedisJob checks a Redis server by issuing a RESP PING.
type PulseRedisJob struct {
	Execution
	EnqueueTime time.Time
	StartTime   time.Time
	Addr        string
	Password    string
	Username    string
	DB          int
	Timeout     time.Duration
	Retries     int
	Entity      ecs.Entity
	ID          uuid.UUID
	payload     map[string]interface{}
}

func newPulseRedisJob(cfg *schema.PulseRedisConfig, timeout time.Duration, entity ecs.Entity) (Job, error) {
	return &PulseRedisJob{
		ID:       uuid.New(),
		Entity:   entity,
		Addr:     cfg.Addr,
		Password: cfg.Password,
		Username: cfg.Username,
		DB:       cfg.DB,
		Timeout:  timeout,
		Retries:  cfg.Retries,
		payload:  map[string]interface{}{"type": "pulse", "driver": "redis"},
	}, nil
}

func (p *PulseRedisJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(result.Err) }()
	ctx, cancel := operationContext(p.Context(), p.Timeout)
	defer cancel()

	payload := p.payload
	client := redis.NewClient(&redis.Options{
		Dialer:     dialBounded,
		Addr:       p.Addr,
		MaxRetries: -1, ContextTimeoutEnabled: true,
		DialTimeout: remaining(ctx), ReadTimeout: remaining(ctx), WriteTimeout: remaining(ctx),
		Password: p.Password,
		Username: p.Username,
		DB:       p.DB,
	})
	defer func() { _ = client.Close() }()

	attempts := p.Retries + 1
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		err := client.Ping(ctx).Err()
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
	return Result{ID: p.ID, Ent: p.Entity, Err: fmt.Errorf("redis check failed for %s after %d attempt(s): %w", p.Addr, attempts, lastErr), Payload: payload}
}

func (p *PulseRedisJob) Copy() Job                  { job := *p; return &job }
func (p *PulseRedisJob) GetEnqueueTime() time.Time  { return p.EnqueueTime }
func (p *PulseRedisJob) SetEnqueueTime(t time.Time) { p.EnqueueTime = t }
func (p *PulseRedisJob) GetStartTime() time.Time    { return p.StartTime }
func (p *PulseRedisJob) SetStartTime(t time.Time)   { p.StartTime = t }
func (p *PulseRedisJob) IsNil() bool                { return p == nil }
