//go:build mysql

package jobs

import (
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"

	"github.com/ziad-hsn/cpra/internal/manifest"
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
	tls         checkTLS
}

func mysqlDSN(cfg *manifest.PulseMySQLConfig) string {
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
	parsed := mysql.NewConfig()
	parsed.User, parsed.Passwd, parsed.Net, parsed.Addr, parsed.DBName = cfg.User, cfg.Password, "tcp", net.JoinHostPort(host, strconv.Itoa(port)), cfg.Database
	return parsed.FormatDSN()
}

func newPulseMySQLJob(cfg *manifest.PulseMySQLConfig, timeout time.Duration, entity ecs.Entity) (Job, error) {
	transport, err := prepareCheckTLS("mysql", cfg, cfg.TLS, cfg.InsecureSkipVerify, cfg.CAFile, cfg.ServerName)
	if err != nil {
		return nil, err
	}
	dsn := mysqlDSN(cfg)
	if _, err := mysqlCheckConfig(dsn); err != nil {
		return nil, err
	}
	return &PulseMySQLJob{
		tls:     transport,
		ID:      uuid.New(),
		Entity:  entity,
		DSN:     dsn,
		Timeout: timeout,
		Retries: cfg.Retries,
		payload: map[string]interface{}{"type": "pulse", "driver": "mysql"},
	}, nil
}

// Parse using the same driver that will initialize the connection. In
// particular, initialization queries must never enable arbitrary LOCAL INFILE.
func mysqlCheckConfig(dsn string) (*mysql.Config, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, errors.New("invalid MySQL connection settings")
	}
	if cfg.AllowAllFiles {
		return nil, errors.New("MySQL allowAllFiles must remain disabled")
	}
	return cfg, nil
}

func (p *PulseMySQLJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(mysqlCheckFailure(result.Err)) }()
	ctx, cancel := operationContext(p.Context(), p.Timeout)
	defer cancel()

	payload := p.payload
	parsed, err := mysqlCheckConfig(p.DSN)
	if err != nil {
		return Result{ID: p.ID, Ent: p.Entity, Err: err, Payload: payload}
	}
	if p.tls.configured {
		if p.tls.enabled && parsed.Net != "tcp" && parsed.Net != "tcp4" && parsed.Net != "tcp6" {
			return Result{ID: p.ID, Ent: p.Entity, Err: errors.New("MySQL TLS requires a TCP connection"), Payload: payload}
		}
		host, _, _ := net.SplitHostPort(parsed.Addr)
		parsed.TLS, err = p.tls.configuration(host)
		if err != nil {
			return Result{ID: p.ID, Ent: p.Entity, Err: err, Payload: payload}
		}
		parsed.TLSConfig = ""
		parsed.AllowFallbackToPlaintext = false
	}
	connector, err := mysql.NewConnector(parsed)
	if err != nil {
		return Result{ID: p.ID, Ent: p.Entity, Err: errors.New("invalid MySQL connection settings"), Payload: payload}
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()

	attempts := newPulseAttempts(ctx, p.Retries)
	defer attempts.complete(&result)
	var lastErr error
	for {
		ctx, ok := attempts.next()
		if !ok {
			break
		}
		err := db.PingContext(ctx)
		if err == nil {
			return Result{ID: p.ID, Ent: p.Entity, Err: nil, Payload: payload}
		}
		lastErr = err
	}
	return Result{ID: p.ID, Ent: p.Entity, Err: fmt.Errorf("mysql check failed after %d attempt(s): %w", attempts.count, lastErr), Payload: payload}
}

func (p *PulseMySQLJob) Copy() Job                  { job := *p; return &job }
func (p *PulseMySQLJob) GetEnqueueTime() time.Time  { return p.EnqueueTime }
func (p *PulseMySQLJob) SetEnqueueTime(t time.Time) { p.EnqueueTime = t }
func (p *PulseMySQLJob) GetStartTime() time.Time    { return p.StartTime }
func (p *PulseMySQLJob) SetStartTime(t time.Time)   { p.StartTime = t }
func (p *PulseMySQLJob) IsNil() bool                { return p == nil }

func mysqlCheckFailure(err error) error {
	var provider *mysql.MySQLError
	if errors.As(err, &provider) {
		switch provider.Number {
		case 1045: // ER_ACCESS_DENIED_ERROR
			return &checkAccessError{code: "authentication_failed", cause: err}
		case 1044, 1142, 1143, 1227: // Database, command, column or specific privilege.
			return &checkAccessError{code: "authorization_failed", cause: err}
		}
	}
	return err
}
