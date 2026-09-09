//go:build postgres

package jobs

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/mlange-42/ark/ecs"

	"cpra/internal/loader/schema"
)

// PulsePostgresJob checks a PostgreSQL server by connecting and issuing a ping.
type PulsePostgresJob struct {
	Execution
	EnqueueTime time.Time
	StartTime   time.Time
	ConnString  string
	Timeout     time.Duration
	Retries     int
	Entity      ecs.Entity
	ID          uuid.UUID
	payload     map[string]interface{}
}

func postgresConnString(cfg *schema.PulsePostgresConfig) string {
	if cfg.DSN != "" {
		return cfg.DSN
	}
	host := cfg.Host
	if host == "" {
		host = "localhost"
	}
	port := cfg.Port
	if port == 0 {
		port = 5432
	}
	sslmode := cfg.SSLMode
	if sslmode == "" {
		sslmode = "prefer"
	}
	quote := func(value string) string {
		return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(value) + "'"
	}
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s", quote(host), port, quote(cfg.User), quote(cfg.Password), quote(cfg.Database), quote(sslmode))
}

func newPulsePostgresJob(cfg *schema.PulsePostgresConfig, timeout time.Duration, entity ecs.Entity) (Job, error) {
	return &PulsePostgresJob{
		ID:         uuid.New(),
		Entity:     entity,
		ConnString: postgresConnString(cfg),
		Timeout:    timeout,
		Retries:    cfg.Retries,
		payload:    map[string]interface{}{"type": "pulse", "driver": "postgres"},
	}, nil
}

func (p *PulsePostgresJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(result.Err) }()
	ctx, cancel := operationContext(p.Context(), p.Timeout)
	defer cancel()

	payload := p.payload
	config, err := pgx.ParseConfig(p.ConnString)
	if err != nil {
		return Result{ID: p.ID, Ent: p.Entity, Err: safeError{err, "invalid postgres connection configuration"}, Payload: payload}
	}
	attempts := p.Retries + 1
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		conn, err := pgx.ConnectConfig(ctx, config)
		if err == nil {
			err = conn.Ping(ctx)
			_ = conn.Close(ctx)
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
	return Result{ID: p.ID, Ent: p.Entity, Err: fmt.Errorf("postgres check failed after %d attempt(s): %w", attempts, lastErr), Payload: payload}
}

func (p *PulsePostgresJob) Copy() Job                  { job := *p; return &job }
func (p *PulsePostgresJob) GetEnqueueTime() time.Time  { return p.EnqueueTime }
func (p *PulsePostgresJob) SetEnqueueTime(t time.Time) { p.EnqueueTime = t }
func (p *PulsePostgresJob) GetStartTime() time.Time    { return p.StartTime }
func (p *PulsePostgresJob) SetStartTime(t time.Time)   { p.StartTime = t }
func (p *PulsePostgresJob) IsNil() bool                { return p == nil }
