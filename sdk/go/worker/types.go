//go:build externaljobs

package worker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

var (
	ErrCapacity       = errors.New("worker journal admission capacity exhausted")
	ErrCorrupt        = errors.New("worker journal is corrupt or cannot be decrypted")
	ErrIdentity       = errors.New("worker server or journal identity mismatch")
	ErrRunning        = errors.New("worker is already running or has run")
	ErrDrainDeadline  = errors.New("worker drain deadline expired; handler still active")
	ErrClosed         = errors.New("worker journal is closed")
	ErrDuplicate      = errors.New("execution already recorded")
	ErrStorage        = errors.New("worker journal durable write failed")
	ErrJournalVersion = errors.New("unsupported worker journal format")
)

// Protocol is implemented by the SDK WorkerClient. Implementations must honor
// context cancellation and the server's durable start/receipt contracts.
type Protocol interface {
	Poll(context.Context, api.PollRequest) (*api.Assignments, error)
	Start(context.Context, api.StartRequest) (*api.StartResponse, error)
	Heartbeat(context.Context, api.HeartbeatRequest) (*api.HeartbeatResponse, error)
	Result(context.Context, api.Outcome) (*api.Receipt, error)
	LateEvidence(context.Context, api.LateEvidenceRequest) (*api.Receipt, error)
}

// CredentialResolver resolves an opaque profile within the worker. Resolved
// values are never persisted by this library or requested from CPRa.
type CredentialResolver func(context.Context, string) (any, error)

// Job contains a copied assignment and worker-local credential resolution.
// Do not include credentials in returned diagnostics, data, or evidence.
type Job struct {
	Assignment  api.Assignment
	Credentials any
}

// Handler returns the server-owned outcome envelope. Execution identity, grant,
// and kind are filled by the runner, never taken from handler output. Errors are
// intentionally not serialized: they may contain provider credentials.
type Handler func(context.Context, Job) (api.Outcome, error)

type handlerKey struct{ id, version, kind string }

// Registry maps exact immutable JobType versions to locally compiled handlers.
// It becomes immutable when a Runner using it starts.
type Registry struct {
	mu       sync.Mutex
	frozen   bool
	handlers map[handlerKey]Handler
}

// NewRegistry constructs an empty registry for locally compiled handlers.
func NewRegistry() *Registry { return &Registry{handlers: make(map[handlerKey]Handler)} }

// Register associates an exact ID, version, and category with a handler.
// It rejects invalid identifiers/categories, duplicates, more than 64 capabilities,
// and changes after Run has started.
func (r *Registry) Register(jobTypeID, version, kind string, handler Handler) error {
	if r == nil {
		return errors.New("nil worker registry")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.frozen {
		return errors.New("worker registry is frozen")
	}
	if !validID(jobTypeID) || !validID(version) || !validKind(kind) || handler == nil {
		return errors.New("job type, version, supported kind and handler are required")
	}
	key := handlerKey{jobTypeID, version, kind}
	if _, ok := r.handlers[key]; ok {
		return errors.New("worker handler already registered")
	}
	if len(r.handlers) >= 64 {
		return errors.New("worker registry exceeds capability limit")
	}
	if r.handlers == nil {
		r.handlers = make(map[handlerKey]Handler)
	}
	r.handlers[key] = handler
	return nil
}

func validKind(kind string) bool {
	return kind == "check" || kind == "recovery" || kind == "notification"
}

func (r *Registry) freeze() (map[handlerKey]Handler, []api.WorkerCapability) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.frozen = true
	m := make(map[handlerKey]Handler, len(r.handlers))
	c := make([]api.WorkerCapability, 0, len(r.handlers))
	for k, h := range r.handlers {
		m[k] = h
		c = append(c, api.WorkerCapability{JobTypeID: k.id, Version: k.version, Kind: k.kind})
	}
	return m, c
}

// Limits bound admission before execution permission is requested. Byte counts
// distinguish reserved payload capacity, live encrypted-record plaintext, and
// the allocated bbolt file. Allocation limits do not interrupt completion writes.
type Limits struct {
	Concurrency     int
	Records         int
	LiveBytes       int64
	OutcomeBytes    int
	DiagnosticBytes int
	AllocatedBytes  int64
}

func (l Limits) defaults() (Limits, error) {
	if l.Concurrency == 0 {
		l.Concurrency = 16
	}
	if l.Records == 0 {
		l.Records = 4096
	}
	if l.LiveBytes == 0 {
		l.LiveBytes = 256 << 20
	}
	if l.OutcomeBytes == 0 {
		l.OutcomeBytes = 128 << 10
	}
	if l.DiagnosticBytes == 0 {
		l.DiagnosticBytes = 64 << 10
	}
	if l.AllocatedBytes == 0 {
		l.AllocatedBytes = 512 << 20
	}
	if l.Concurrency < 1 || l.Concurrency > 100 || l.Records < 1 || l.Records > 1_000_000 || l.LiveBytes < 1 || l.LiveBytes > 1<<50 || l.OutcomeBytes < 1024 || l.OutcomeBytes > 128<<10 || l.DiagnosticBytes < 1 || l.DiagnosticBytes > 64<<10 || l.DiagnosticBytes > l.OutcomeBytes || l.AllocatedBytes < 1 || l.AllocatedBytes > 1<<50 {
		return l, errors.New("invalid worker limits")
	}
	return l, nil
}

// Config requires absolute state/key paths, a private 32-byte key file outside
// StateDir, and the expected CPRa store/restore identity. No key is generated or
// replaced automatically. Use a separate state directory for each worker.
type Config struct {
	Client            Protocol
	Registry          *Registry
	WorkerID          string
	WorkerUID         string // Immutable provisioned worker incarnation; required on every restart.
	ServerID          string
	StateDir          string
	WrappingKeyPath   string
	Credentials       CredentialResolver
	Limits            Limits
	DrainTimeout      time.Duration
	RetryInterval     time.Duration
	PollWait          time.Duration
	HeartbeatInterval time.Duration
}

func (c Config) validate() (Config, error) {
	if c.Client == nil || c.Registry == nil || !validID(c.WorkerID) || !validID(c.WorkerUID) || !validID(c.ServerID) {
		return c, errors.New("worker client, registry, worker identity and server identity are required")
	}
	var err error
	c.Limits, err = c.Limits.defaults()
	if err != nil {
		return c, err
	}
	if c.DrainTimeout == 0 {
		c.DrainTimeout = 45 * time.Second
	}
	if c.RetryInterval == 0 {
		c.RetryInterval = time.Second
	}
	if c.PollWait == 0 {
		c.PollWait = 25 * time.Second
	}
	if c.HeartbeatInterval == 0 {
		c.HeartbeatInterval = 10 * time.Second
	}
	if c.DrainTimeout < 0 || c.RetryInterval < 0 || c.PollWait < time.Second || c.PollWait > 25*time.Second || c.HeartbeatInterval < 0 {
		return c, fmt.Errorf("invalid worker timing settings")
	}
	return c, nil
}

// Status reports bounded worker activity and journal usage. Allocated bytes can
// exceed live bytes because removing records does not shrink the database file.
type Status struct {
	Running              bool
	Draining             bool
	DrainDeadlineExpired bool
	ActiveHandlers       int
	PendingOutcomes      int
	UnknownActions       int
	Records              int
	AllocatedBytes       int64
	LiveBytes            int64
	ReservedBytes        int64
	AdmissionAvailable   bool
	AdmissionCapacity    int
	LastError            error
}
