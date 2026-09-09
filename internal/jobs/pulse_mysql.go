//go:build mysql

package jobs

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"

	"cpra/internal/loader/schema"
)

// PulseMySQLJob checks a MySQL server by connecting and issuing a COM_PING.
type PulseMySQLJob struct {
	Execution
	EnqueueTime time.Time
	StartTime   time.Time
	DSN         string
	Timeout     time.Duration
	Retries     int
	Entity      ecs.Entity
	ID          uuid.UUID
	payload     map[string]interface{}
}

func mysqlDSN(cfg *schema.PulseMySQLConfig) string {
	if cfg.DSN != "" {
		return cfg.DSN
	}
	host := cfg.Host
	if host == "" {
		host = "localhost"
	}
	port := cfg.Port
	if port == 0 {
		port = 3306
	}
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s", cfg.User, cfg.Password, host, port, cfg.Database)
}

func newPulseMySQLJob(cfg *schema.PulseMySQLConfig, timeout time.Duration, entity ecs.Entity) (Job, error) {
	return &PulseMySQLJob{
		ID:      uuid.New(),
		Entity:  entity,
		DSN:     mysqlDSN(cfg),
		Timeout: timeout,
		Retries: cfg.Retries,
		payload: map[string]interface{}{"type": "pulse", "driver": "mysql"},
	}, nil
}

func (p *PulseMySQLJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(result.Err) }()
	ctx, cancel := operationContext(p.Context(), p.Timeout)
	defer cancel()

	payload := p.payload
	db, err := sql.Open("mysql", p.DSN)
	if err != nil {
		return Result{ID: p.ID, Ent: p.Entity, Err: fmt.Errorf("failed to open mysql connection: %w", err), Payload: payload}
	}
	defer func() { _ = db.Close() }()

	attempts := p.Retries + 1
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		err := db.PingContext(ctx)
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
	return Result{ID: p.ID, Ent: p.Entity, Err: fmt.Errorf("mysql check failed after %d attempt(s): %w", attempts, lastErr), Payload: payload}
}

func (p *PulseMySQLJob) Copy() Job                  { job := *p; return &job }
func (p *PulseMySQLJob) GetEnqueueTime() time.Time  { return p.EnqueueTime }
func (p *PulseMySQLJob) SetEnqueueTime(t time.Time) { p.EnqueueTime = t }
func (p *PulseMySQLJob) GetStartTime() time.Time    { return p.StartTime }
func (p *PulseMySQLJob) SetStartTime(t time.Time)   { p.StartTime = t }
func (p *PulseMySQLJob) IsNil() bool                { return p == nil }
