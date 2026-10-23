//go:build postgres

package jobs

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mlange-42/ark/ecs"

	"github.com/ziad-hsn/cpra/internal/manifest"
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
	tls         checkTLS
}

func postgresConnString(cfg *manifest.PulsePostgresConfig) string {
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

func newPulsePostgresJob(cfg *manifest.PulsePostgresConfig, timeout time.Duration, entity ecs.Entity) (Job, error) {
	transport, err := prepareCheckTLS("postgres", cfg, cfg.TLS, cfg.InsecureSkipVerify, cfg.CAFile, cfg.ServerName)
	if err != nil {
		return nil, err
	}
	return &PulsePostgresJob{
		tls:        transport,
		ID:         uuid.New(),
		Entity:     entity,
		ConnString: postgresConnString(cfg),
		Timeout:    timeout,
		Retries:    cfg.Retries,
		payload:    map[string]interface{}{"type": "pulse", "driver": "postgres"},
	}, nil
}

func (p *PulsePostgresJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(postgresCheckFailure(result.Err)) }()
	ctx, cancel := operationContext(p.Context(), p.Timeout)
	defer cancel()

	payload := p.payload
	connection := p.ConnString
	if p.tls.configured {
		// Parse host/authentication without ambient or DSN TLS fallbacks. The
		// validated explicit policy is installed for every host below.
		if strings.HasPrefix(connection, "postgres://") || strings.HasPrefix(connection, "postgresql://") {
			u, err := url.Parse(connection)
			if err != nil {
				return Result{ID: p.ID, Ent: p.Entity, Err: fmt.Errorf("invalid PostgreSQL connection settings"), Payload: payload}
			}
			query := u.Query()
			query.Set("sslmode", "disable")
			u.RawQuery = query.Encode()
			connection = u.String()
		} else {
			connection += " sslmode=disable"
		}
	}
	config, err := pgx.ParseConfig(connection)
	if err != nil {
		return Result{ID: p.ID, Ent: p.Entity, Err: safeError{err, "invalid postgres connection configuration"}, Payload: payload}
	}
	if p.tls.configured {
		if p.tls.enabled && strings.HasPrefix(config.Host, "/") {
			return Result{ID: p.ID, Ent: p.Entity, Err: fmt.Errorf("PostgreSQL TLS requires TCP hosts"), Payload: payload}
		}
		config.TLSConfig, err = p.tls.configuration(config.Host)
		if err != nil {
			return Result{ID: p.ID, Ent: p.Entity, Err: err, Payload: payload}
		}
		for _, fallback := range config.Fallbacks {
			if p.tls.enabled && strings.HasPrefix(fallback.Host, "/") {
				return Result{ID: p.ID, Ent: p.Entity, Err: fmt.Errorf("PostgreSQL TLS requires TCP hosts"), Payload: payload}
			}
			if config.TLSConfig == nil {
				fallback.TLSConfig = nil
				continue
			}
			fallback.TLSConfig = config.TLSConfig.Clone()
			if p.tls.serverName == "" {
				fallback.TLSConfig.ServerName = fallback.Host
			}
		}
	}
	attempts := newPulseAttempts(ctx, p.Retries)
	defer attempts.complete(&result)
	var lastErr error
	for {
		ctx, ok := attempts.next()
		if !ok {
			break
		}
		conn, err := pgx.ConnectConfig(ctx, config)
		if err == nil {
			err = conn.Ping(ctx)
			_ = conn.Close(ctx)
		}
		if err == nil {
			return Result{ID: p.ID, Ent: p.Entity, Err: nil, Payload: payload}
		}
		lastErr = err
	}
	return Result{ID: p.ID, Ent: p.Entity, Err: fmt.Errorf("postgres check failed after %d attempt(s): %w", attempts.count, lastErr), Payload: payload}
}

func (p *PulsePostgresJob) Copy() Job                  { job := *p; return &job }
func (p *PulsePostgresJob) GetEnqueueTime() time.Time  { return p.EnqueueTime }
func (p *PulsePostgresJob) SetEnqueueTime(t time.Time) { p.EnqueueTime = t }
func (p *PulsePostgresJob) GetStartTime() time.Time    { return p.StartTime }
func (p *PulsePostgresJob) SetStartTime(t time.Time)   { p.StartTime = t }
func (p *PulsePostgresJob) IsNil() bool                { return p == nil }

func postgresCheckFailure(err error) error {
	var provider *pgconn.PgError
	if errors.As(err, &provider) {
		switch provider.Code {
		case "28000", "28P01":
			return &checkAccessError{code: "authentication_failed", cause: err}
		case "42501":
			return &checkAccessError{code: "authorization_failed", cause: err}
		}
	}
	return err
}
