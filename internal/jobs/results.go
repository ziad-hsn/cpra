package jobs

import (
	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"
)

// Result is a generic structure for returning the outcome of a job.
// It includes the entity it belongs to, any error that occurred, and a flexible payload.
type Result struct {
	Err     error
	Payload map[string]interface{}
	Ent     ecs.Entity
	ID      uuid.UUID
}

// Entity returns the entity associated with the result.
func (r *Result) Entity() ecs.Entity {
	return r.Ent
}

// Error returns the error associated with the result, if any.
func (r *Result) Error() error {
	return r.Err
}

// Pre-allocated payloads for common job types to reduce allocations in hot paths.
// Only payloads that remain immutable are shared; mutable ones use pooling.
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

// GetPulseHTTPPayload returns the pre-allocated pulse HTTP payload.
func GetPulseHTTPPayload() map[string]interface{} { return pulseHTTPPayload }

// GetPulseTCPPayload returns the pre-allocated pulse TCP payload.
func GetPulseTCPPayload() map[string]interface{} { return pulseTCPPayload }

// GetInterventionDockerPayload returns the pre-allocated intervention Docker payload.
func GetInterventionDockerPayload() map[string]interface{} { return interventionDockerPayload }

// Note: ICMP payloads are NOT pooled because the payload map escapes in the Result
// struct and its lifetime extends beyond the Execute function. RouteResults reads
// from result.Payload["type"] asynchronously, so we cannot recycle the map.
