package jobs

import (
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"

	"cpra/internal/loader/schema"
)

// PulseTLSJob checks a TLS endpoint by completing a handshake and reports
// days-to-expiry of the leaf certificate. Pure stdlib, always compiled.
type PulseTLSJob struct {
	Execution
	EnqueueTime        time.Time
	StartTime          time.Time
	Host               string
	Port               int
	ServerName         string
	WarnDays           int
	CriticalDays       int
	InsecureSkipVerify bool
	Timeout            time.Duration
	Retries            int
	Entity             ecs.Entity
	ID                 uuid.UUID
	payload            map[string]interface{}
}

func newPulseTLSJob(cfg *schema.PulseTLSConfig, timeout time.Duration, entity ecs.Entity) (Job, error) {
	serverName := cfg.ServerName
	if serverName == "" {
		serverName = cfg.Host
	}
	return &PulseTLSJob{
		ID:                 uuid.New(),
		Entity:             entity,
		Host:               cfg.Host,
		Port:               cfg.Port,
		ServerName:         serverName,
		WarnDays:           cfg.WarnDays,
		CriticalDays:       cfg.CriticalDays,
		InsecureSkipVerify: cfg.InsecureSkipVerify,
		Timeout:            timeout,
		Retries:            cfg.Retries,
		payload:            map[string]interface{}{"type": "pulse", "driver": "tls"},
	}, nil
}

func (p *PulseTLSJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(result.Err) }()
	ctx, cancel := operationContext(p.Context(), p.Timeout)
	defer cancel()

	payload := p.payload
	attempts := p.Retries + 1
	if attempts < 1 {
		attempts = 1
	}
	addr := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
	dialer := &net.Dialer{Timeout: p.Timeout}
	tlsCfg := &tls.Config{ServerName: p.ServerName, InsecureSkipVerify: p.InsecureSkipVerify}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		conn, err := (&tls.Dialer{NetDialer: dialer, Config: tlsCfg}).DialContext(ctx, "tcp", addr)
		if err != nil {
			lastErr = err
			if attempt < attempts-1 {
				if !retryDelay(ctx) {
					break
				}
			}
			continue
		}
		state := conn.(*tls.Conn).ConnectionState()
		_ = conn.Close()
		if len(state.PeerCertificates) == 0 {
			return Result{ID: p.ID, Ent: p.Entity, Err: fmt.Errorf("tls handshake succeeded but no peer certificate presented"), Payload: payload}
		}
		leaf := state.PeerCertificates[0]
		daysLeft := int(time.Until(leaf.NotAfter).Hours() / 24)
		out := map[string]interface{}{
			"type":      "pulse",
			"driver":    "tls",
			"days_left": daysLeft,
			"not_after": leaf.NotAfter,
		}
		if p.CriticalDays > 0 && daysLeft <= p.CriticalDays {
			return Result{ID: p.ID, Ent: p.Entity, Err: fmt.Errorf("tls certificate for %s expires in %d day(s) (critical threshold %d)", addr, daysLeft, p.CriticalDays), Payload: out}
		}
		if p.WarnDays > 0 && daysLeft <= p.WarnDays {
			return Result{ID: p.ID, Ent: p.Entity, Warning: fmt.Sprintf("tls certificate expires in %d day(s) (warning threshold %d)", daysLeft, p.WarnDays), Payload: out}
		}
		return Result{ID: p.ID, Ent: p.Entity, Err: nil, Payload: out}
	}
	return Result{ID: p.ID, Ent: p.Entity, Err: fmt.Errorf("tls check failed for %s after %d attempt(s): %w", addr, attempts, lastErr), Payload: payload}
}

func (p *PulseTLSJob) Copy() Job                  { job := *p; return &job }
func (p *PulseTLSJob) GetEnqueueTime() time.Time  { return p.EnqueueTime }
func (p *PulseTLSJob) SetEnqueueTime(t time.Time) { p.EnqueueTime = t }
func (p *PulseTLSJob) GetStartTime() time.Time    { return p.StartTime }
func (p *PulseTLSJob) SetStartTime(t time.Time)   { p.StartTime = t }
func (p *PulseTLSJob) IsNil() bool                { return p == nil }
