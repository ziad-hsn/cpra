package jobs

import (
	"time"

	"github.com/mlange-42/ark/ecs"
)

// JobResult is the interface for type-safe job results.
// Implementations provide type-specific result data while maintaining
// a common interface for routing and processing.
//
// This replaces the generic Result struct with map[string]interface{}
// payloads, enabling:
//   - Compile-time type safety
//   - No string-based type discrimination
//   - Cleaner result processing in ECS systems
type JobResult interface {
	// Entity returns the ECS entity associated with this result.
	Entity() ecs.Entity

	// Error returns any error that occurred during job execution.
	Error() error

	// JobType returns the type of job ("pulse", "intervention", "code").
	JobType() string

	// ToLegacyResult converts to the legacy Result type for backward compatibility.
	// This enables incremental migration without breaking existing code.
	ToLegacyResult() Result
}

// PulseResult contains the outcome of a pulse health check.
type PulseResult struct {
	Ent        ecs.Entity
	Err        error
	Driver     string        // "http", "tcp", "icmp"
	StatusCode int           // HTTP status code (0 for non-HTTP)
	Latency    time.Duration // Request latency
}

func (r *PulseResult) Entity() ecs.Entity { return r.Ent }
func (r *PulseResult) Error() error       { return r.Err }
func (r *PulseResult) JobType() string    { return "pulse" }

// ToLegacyResult converts to the legacy Result type.
func (r *PulseResult) ToLegacyResult() Result {
	payload := map[string]interface{}{
		"type":   "pulse",
		"driver": r.Driver,
	}
	if r.StatusCode > 0 {
		payload["status_code"] = r.StatusCode
	}
	if r.Latency > 0 {
		payload["latency_ms"] = r.Latency.Milliseconds()
	}
	return Result{
		Ent:     r.Ent,
		Err:     r.Err,
		Payload: payload,
	}
}

// InterventionResult contains the outcome of an intervention action.
type InterventionResult struct {
	Ent       ecs.Entity
	Err       error
	Driver    string // "docker"
	Action    string // "restart", "stop", "start", "kill", "pause", "unpause", "scale"
	Container string // Container name/ID
	Duration  time.Duration
}

func (r *InterventionResult) Entity() ecs.Entity { return r.Ent }
func (r *InterventionResult) Error() error       { return r.Err }
func (r *InterventionResult) JobType() string    { return "intervention" }

// ToLegacyResult converts to the legacy Result type.
func (r *InterventionResult) ToLegacyResult() Result {
	payload := map[string]interface{}{
		"type":   "intervention",
		"driver": r.Driver,
	}
	if r.Action != "" {
		payload["action"] = r.Action
	}
	if r.Container != "" {
		payload["container"] = r.Container
	}
	if r.Duration > 0 {
		payload["duration_ms"] = r.Duration.Milliseconds()
	}
	return Result{
		Ent:     r.Ent,
		Err:     r.Err,
		Payload: payload,
	}
}

// CodeResult contains the outcome of a code alert notification.
type CodeResult struct {
	Ent       ecs.Entity
	Err       error
	Driver    string // "log", "slack", "pagerduty", "email", "webhook"
	Color     string // "red", "yellow", "green", "cyan", "gray"
	Delivered bool   // Whether the notification was successfully delivered
}

func (r *CodeResult) Entity() ecs.Entity { return r.Ent }
func (r *CodeResult) Error() error       { return r.Err }
func (r *CodeResult) JobType() string    { return "code" }

// ToLegacyResult converts to the legacy Result type.
func (r *CodeResult) ToLegacyResult() Result {
	payload := map[string]interface{}{
		"type":   "code",
		"driver": r.Driver,
	}
	if r.Color != "" {
		payload["color"] = r.Color
	}
	if r.Delivered {
		payload["delivered"] = true
	}
	return Result{
		Ent:     r.Ent,
		Err:     r.Err,
		Payload: payload,
	}
}

// NewPulseResult creates a new PulseResult.
func NewPulseResult(ent ecs.Entity, err error, driver string) *PulseResult {
	return &PulseResult{
		Ent:    ent,
		Err:    err,
		Driver: driver,
	}
}

// NewPulseResultWithLatency creates a PulseResult with latency information.
func NewPulseResultWithLatency(ent ecs.Entity, err error, driver string, latency time.Duration) *PulseResult {
	return &PulseResult{
		Ent:     ent,
		Err:     err,
		Driver:  driver,
		Latency: latency,
	}
}

// NewHTTPPulseResult creates a PulseResult for HTTP checks.
func NewHTTPPulseResult(ent ecs.Entity, err error, statusCode int, latency time.Duration) *PulseResult {
	return &PulseResult{
		Ent:        ent,
		Err:        err,
		Driver:     "http",
		StatusCode: statusCode,
		Latency:    latency,
	}
}

// NewInterventionResult creates a new InterventionResult.
func NewInterventionResult(ent ecs.Entity, err error, driver, action string) *InterventionResult {
	return &InterventionResult{
		Ent:    ent,
		Err:    err,
		Driver: driver,
		Action: action,
	}
}

// NewDockerInterventionResult creates an InterventionResult for Docker actions.
func NewDockerInterventionResult(ent ecs.Entity, err error, action, container string) *InterventionResult {
	return &InterventionResult{
		Ent:       ent,
		Err:       err,
		Driver:    "docker",
		Action:    action,
		Container: container,
	}
}

// NewCodeResult creates a new CodeResult.
func NewCodeResult(ent ecs.Entity, err error, driver, color string) *CodeResult {
	return &CodeResult{
		Ent:    ent,
		Err:    err,
		Driver: driver,
		Color:  color,
	}
}

// NewCodeResultDelivered creates a CodeResult marking successful delivery.
func NewCodeResultDelivered(ent ecs.Entity, driver, color string) *CodeResult {
	return &CodeResult{
		Ent:       ent,
		Err:       nil,
		Driver:    driver,
		Color:     color,
		Delivered: true,
	}
}

// ConvertJobResultToLegacy converts any JobResult to the legacy Result type.
// This is useful during the migration period.
func ConvertJobResultToLegacy(jr JobResult) Result {
	if jr == nil {
		return Result{}
	}
	return jr.ToLegacyResult()
}

// FromLegacyResult attempts to convert a legacy Result to a typed JobResult.
// Returns nil if the result type cannot be determined.
func FromLegacyResult(r Result) JobResult {
	if r.Payload == nil {
		return nil
	}

	jobType, ok := r.Payload["type"].(string)
	if !ok {
		return nil
	}

	switch jobType {
	case "pulse":
		driver, _ := r.Payload["driver"].(string)
		result := &PulseResult{
			Ent:    r.Ent,
			Err:    r.Err,
			Driver: driver,
		}
		if sc, ok := r.Payload["status_code"].(int); ok {
			result.StatusCode = sc
		}
		if lat, ok := r.Payload["latency_ms"].(int64); ok {
			result.Latency = time.Duration(lat) * time.Millisecond
		}
		return result

	case "intervention":
		driver, _ := r.Payload["driver"].(string)
		action, _ := r.Payload["action"].(string)
		container, _ := r.Payload["container"].(string)
		result := &InterventionResult{
			Ent:       r.Ent,
			Err:       r.Err,
			Driver:    driver,
			Action:    action,
			Container: container,
		}
		if dur, ok := r.Payload["duration_ms"].(int64); ok {
			result.Duration = time.Duration(dur) * time.Millisecond
		}
		return result

	case "code":
		driver, _ := r.Payload["driver"].(string)
		color, _ := r.Payload["color"].(string)
		delivered, _ := r.Payload["delivered"].(bool)
		return &CodeResult{
			Ent:       r.Ent,
			Err:       r.Err,
			Driver:    driver,
			Color:     color,
			Delivered: delivered,
		}

	default:
		return nil
	}
}
