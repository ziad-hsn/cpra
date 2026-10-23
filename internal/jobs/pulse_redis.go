//go:build redis

package jobs

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"
	"github.com/redis/go-redis/v9"

	"github.com/ziad-hsn/cpra/internal/manifest"
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
	tls         checkTLS
}

func newPulseRedisJob(cfg *manifest.PulseRedisConfig, timeout time.Duration, entity ecs.Entity) (Job, error) {
	transport, err := prepareCheckTLS("redis", cfg, cfg.TLS, cfg.InsecureSkipVerify, cfg.CAFile, cfg.ServerName)
	if err != nil {
		return nil, err
	}
	return &PulseRedisJob{
		tls:      transport,
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
	defer func() { result.Err = SanitizeError(redisCheckFailure(result.Err)) }()
	ctx, cancel := operationContext(p.Context(), p.Timeout)
	defer cancel()

	payload := p.payload
	options, err := p.clientOptions(ctx)
	if err != nil {
		return Result{ID: p.ID, Ent: p.Entity, Err: err, Payload: payload}
	}
	client := redis.NewClient(options)
	defer func() { _ = client.Close() }()

	attempts := newPulseAttempts(ctx, p.Retries)
	defer attempts.complete(&result)
	var lastErr error
	for {
		ctx, ok := attempts.next()
		if !ok {
			break
		}
		err := client.Ping(ctx).Err()
		if err == nil {
			return Result{ID: p.ID, Ent: p.Entity, Payload: payload}
		}
		lastErr = err
	}
	return Result{ID: p.ID, Ent: p.Entity, Err: fmt.Errorf("redis check failed after %d attempt(s): %w", attempts.count, lastErr), Payload: payload}
}

func (p *PulseRedisJob) clientOptions(ctx context.Context) (*redis.Options, error) {
	host := ""
	if p.tls.enabled {
		address := p.Addr
		if address == "" {
			address = "localhost:6379"
		}
		var err error
		host, _, err = net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("Redis TLS requires a TCP address")
		}
	}
	tlsConfig, err := p.tls.configuration(host)
	if err != nil {
		return nil, err
	}
	return &redis.Options{
		Dialer: func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialCheckTLS(ctx, network, address, tlsConfig)
		},
		Addr:       p.Addr,
		MaxRetries: -1, ContextTimeoutEnabled: true,
		DialTimeout: remaining(ctx), ReadTimeout: remaining(ctx), WriteTimeout: remaining(ctx),
		Password: p.Password,
		Username: p.Username,
		DB:       p.DB,
	}, nil
}

func (p *PulseRedisJob) Copy() Job                  { job := *p; return &job }
func (p *PulseRedisJob) GetEnqueueTime() time.Time  { return p.EnqueueTime }
func (p *PulseRedisJob) SetEnqueueTime(t time.Time) { p.EnqueueTime = t }
func (p *PulseRedisJob) GetStartTime() time.Time    { return p.StartTime }
func (p *PulseRedisJob) SetStartTime(t time.Time)   { p.StartTime = t }
func (p *PulseRedisJob) IsNil() bool                { return p == nil }

func redisCheckFailure(err error) error {
	var provider redis.Error
	if !errors.As(err, &provider) {
		return err
	}
	// RESP defines the first token as its protocol error code. Never inspect
	// ordinary error strings or infer identity failures from message substrings.
	code, _, _ := strings.Cut(provider.Error(), " ")
	switch code {
	case "NOAUTH", "WRONGPASS":
		return &checkAccessError{code: "authentication_failed", cause: err}
	case "NOPERM":
		return &checkAccessError{code: "authorization_failed", cause: err}
	}
	return err
}
