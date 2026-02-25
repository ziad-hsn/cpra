package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/mlange-42/ark/ecs"

	"cpra/internal/interning"
)

// Job defines the interface for any executable task in the system.
//
// All job implementations must be safe for concurrent execution. Jobs are
// typically created via factory functions, enqueued, dequeued by worker pools,
// executed, and results are processed by ECS systems.
//
// Jobs should implement Copy() to allow safe reuse of job pools. The IsNil()
// method allows checking for nil jobs without type assertions.
type Job interface {
	Execute(ctx context.Context) Result
	Copy() Job
	GetEnqueueTime() time.Time
	SetEnqueueTime(time.Time)
	GetStartTime() time.Time
	SetStartTime(time.Time)
	IsNil() bool
}

// Resettable interface allows jobs to be reset for pool reuse.
type Resettable interface {
	Reset()
}

// Result is a generic structure for returning the outcome of a job.
// It includes the entity it belongs to, any error that occurred, and a flexible payload.
type Result struct {
	Err     error
	Payload map[string]interface{}
	Ent     ecs.Entity
}

// Entity returns the entity associated with the result.
func (r *Result) Entity() ecs.Entity {
	return r.Ent
}

// Error returns the error associated with the result, if any.
func (r *Result) Error() error {
	return r.Err
}

// Predefined errors for job creation and execution.
// Using predeclared errors avoids allocations in hot paths.
var (
	// Factory errors
	ErrUnknownPulseConfig        = errors.New("unknown pulse config type")
	ErrDockerMissingTarget       = errors.New("docker intervention missing target configuration")
	ErrUnknownInterventionAction = errors.New("unknown intervention action")
	ErrUnknownCodeNotification   = errors.New("unknown code notification type")

	// Execution errors - pulse jobs
	ErrHTTPNon2xxStatus = errors.New("received non-2xx status code")
	ErrHTTPCheckFailed  = errors.New("http check failed after retries")
	ErrTCPCheckFailed   = errors.New("tcp check failed after retries")
	ErrICMPCheckFailed  = errors.New("icmp check failed after retries")
	ErrNoPackets        = errors.New("no packets received")

	// Execution errors - intervention jobs
	ErrFailedToCreateDockerClient = errors.New("failed to create docker client")
	ErrDockerActionFailed         = errors.New("docker intervention failed after retries")
	ErrDockerStopFailed           = errors.New("docker stop failed after retries")
	ErrDockerStartFailed          = errors.New("docker start failed after retries")
	ErrDockerKillFailed           = errors.New("docker kill failed after retries")
	ErrDockerPauseFailed          = errors.New("docker pause failed after retries")
	ErrDockerUnpauseFailed        = errors.New("docker unpause failed after retries")
	ErrDockerScaleFailed          = errors.New("docker scale failed after retries")
	ErrNotReplicatedService       = errors.New("service is not in replicated mode")
	ErrUnknownDockerAction        = errors.New("unknown docker action type")

	// Execution errors - code jobs
	ErrLogMarshalFailed = errors.New("failed to marshal log entry")

	// Resource limit errors
	ErrSemaphoreTimeout   = errors.New("ICMP semaphore acquire timeout")
	ErrDialLimiterTimeout = errors.New("dial limiter timeout (system under load)")

	// Deprecated - kept for backwards compatibility
	ErrFailedToCreateHTTPRequest = errors.New("failed to create http request")
)

// Pre-interned strings for job types and drivers to reduce allocations.
// These are used in job creation to avoid string allocations.
var (
	InternedPulse        = interning.Intern("pulse")
	InternedIntervention = interning.Intern("intervention")
	InternedCode         = interning.Intern("code")
	InternedHTTP         = interning.Intern("http")
	InternedTCP          = interning.Intern("tcp")
	InternedICMP         = interning.Intern("icmp")
	InternedDocker       = interning.Intern("docker")
)

// Pre-allocated payloads for common job types to reduce allocations in hot paths.
// Only payloads that remain immutable are shared; mutable ones use fresh maps.
var (
	// Pulse job payloads
	pulseHTTPPayload = map[string]interface{}{"type": "pulse", "driver": "http"}
	pulseTCPPayload  = map[string]interface{}{"type": "pulse", "driver": "tcp"}

	// Intervention job payloads
	interventionDockerPayload = map[string]interface{}{"type": "intervention", "driver": "docker"}

	// Code job payloads (base - colors added dynamically)
	codeLogPayload       = map[string]interface{}{"type": "code", "driver": "log"}
	codePagerDutyPayload = map[string]interface{}{"type": "code", "driver": "pagerduty"}
	codeSlackPayload     = map[string]interface{}{"type": "code", "driver": "slack"}
	codeEmailPayload     = map[string]interface{}{"type": "code", "driver": "email"}
	codeWebhookPayload   = map[string]interface{}{"type": "code", "driver": "webhook"}
)

// Payload getters - return pre-allocated payloads for common job types.
// Note: ICMP payloads are NOT pooled because the payload map escapes in the Result
// struct and its lifetime extends beyond the Execute function.

// GetPulseHTTPPayload returns the pre-allocated pulse HTTP payload.
func GetPulseHTTPPayload() map[string]interface{} { return pulseHTTPPayload }

// GetPulseTCPPayload returns the pre-allocated pulse TCP payload.
func GetPulseTCPPayload() map[string]interface{} { return pulseTCPPayload }

// GetInterventionDockerPayload returns the pre-allocated intervention Docker payload.
func GetInterventionDockerPayload() map[string]interface{} { return interventionDockerPayload }

// GetCodeLogPayload returns the pre-allocated code log payload.
func GetCodeLogPayload() map[string]interface{} { return codeLogPayload }

// GetCodePagerDutyPayload returns the pre-allocated code PagerDuty payload.
func GetCodePagerDutyPayload() map[string]interface{} { return codePagerDutyPayload }

// GetCodeSlackPayload returns the pre-allocated code Slack payload.
func GetCodeSlackPayload() map[string]interface{} { return codeSlackPayload }

// GetCodeEmailPayload returns the pre-allocated code email payload.
func GetCodeEmailPayload() map[string]interface{} { return codeEmailPayload }

// GetCodeWebhookPayload returns the pre-allocated code webhook payload.
func GetCodeWebhookPayload() map[string]interface{} { return codeWebhookPayload }
