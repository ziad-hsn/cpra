package jobs

import (
	"errors"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// TestPulseResult_Interface verifies PulseResult implements JobResult
func TestPulseResult_Interface(t *testing.T) {
	t.Parallel()
	var _ JobResult = (*PulseResult)(nil)
}

// TestInterventionResult_Interface verifies InterventionResult implements JobResult
func TestInterventionResult_Interface(t *testing.T) {
	t.Parallel()
	var _ JobResult = (*InterventionResult)(nil)
}

// TestCodeResult_Interface verifies CodeResult implements JobResult
func TestCodeResult_Interface(t *testing.T) {
	t.Parallel()
	var _ JobResult = (*CodeResult)(nil)
}

// TestPulseResult_Methods tests PulseResult method implementations
func TestPulseResult_Methods(t *testing.T) {
	t.Parallel()
	testErr := errors.New("test error")
	ent := ecs.Entity{}

	r := &PulseResult{
		Ent:        ent,
		Err:        testErr,
		Driver:     "http",
		StatusCode: 200,
		Latency:    100 * time.Millisecond,
	}

	if r.Entity() != ent {
		t.Error("Entity() should return the entity")
	}
	if r.Error() != testErr {
		t.Error("Error() should return the error")
	}
	if r.JobType() != "pulse" {
		t.Errorf("JobType() = %q, want %q", r.JobType(), "pulse")
	}
}

// TestInterventionResult_Methods tests InterventionResult method implementations
func TestInterventionResult_Methods(t *testing.T) {
	t.Parallel()
	testErr := errors.New("docker failed")
	ent := ecs.Entity{}

	r := &InterventionResult{
		Ent:       ent,
		Err:       testErr,
		Driver:    "docker",
		Action:    "restart",
		Container: "my-container",
		Duration:  5 * time.Second,
	}

	if r.Entity() != ent {
		t.Error("Entity() should return the entity")
	}
	if r.Error() != testErr {
		t.Error("Error() should return the error")
	}
	if r.JobType() != "intervention" {
		t.Errorf("JobType() = %q, want %q", r.JobType(), "intervention")
	}
}

// TestCodeResult_Methods tests CodeResult method implementations
func TestCodeResult_Methods(t *testing.T) {
	t.Parallel()
	ent := ecs.Entity{}

	r := &CodeResult{
		Ent:       ent,
		Err:       nil,
		Driver:    "slack",
		Color:     "red",
		Delivered: true,
	}

	if r.Entity() != ent {
		t.Error("Entity() should return the entity")
	}
	if r.Error() != nil {
		t.Error("Error() should return nil")
	}
	if r.JobType() != "code" {
		t.Errorf("JobType() = %q, want %q", r.JobType(), "code")
	}
}

// TestPulseResult_ToLegacyResult tests conversion to legacy Result
func TestPulseResult_ToLegacyResult(t *testing.T) {
	t.Parallel()
	ent := ecs.Entity{}
	testErr := errors.New("check failed")

	r := &PulseResult{
		Ent:        ent,
		Err:        testErr,
		Driver:     "http",
		StatusCode: 500,
		Latency:    150 * time.Millisecond,
	}

	legacy := r.ToLegacyResult()

	if legacy.Ent != ent {
		t.Error("Legacy result should have same entity")
	}
	if legacy.Err != testErr {
		t.Error("Legacy result should have same error")
	}
	if legacy.Payload["type"] != "pulse" {
		t.Errorf("Payload[type] = %v, want pulse", legacy.Payload["type"])
	}
	if legacy.Payload["driver"] != "http" {
		t.Errorf("Payload[driver] = %v, want http", legacy.Payload["driver"])
	}
	if legacy.Payload["status_code"] != 500 {
		t.Errorf("Payload[status_code] = %v, want 500", legacy.Payload["status_code"])
	}
	if legacy.Payload["latency_ms"] != int64(150) {
		t.Errorf("Payload[latency_ms] = %v, want 150", legacy.Payload["latency_ms"])
	}
}

// TestInterventionResult_ToLegacyResult tests conversion to legacy Result
func TestInterventionResult_ToLegacyResult(t *testing.T) {
	t.Parallel()
	ent := ecs.Entity{}

	r := &InterventionResult{
		Ent:       ent,
		Err:       nil,
		Driver:    "docker",
		Action:    "restart",
		Container: "web-server",
		Duration:  2 * time.Second,
	}

	legacy := r.ToLegacyResult()

	if legacy.Payload["type"] != "intervention" {
		t.Errorf("Payload[type] = %v, want intervention", legacy.Payload["type"])
	}
	if legacy.Payload["driver"] != "docker" {
		t.Errorf("Payload[driver] = %v, want docker", legacy.Payload["driver"])
	}
	if legacy.Payload["action"] != "restart" {
		t.Errorf("Payload[action] = %v, want restart", legacy.Payload["action"])
	}
	if legacy.Payload["container"] != "web-server" {
		t.Errorf("Payload[container] = %v, want web-server", legacy.Payload["container"])
	}
	if legacy.Payload["duration_ms"] != int64(2000) {
		t.Errorf("Payload[duration_ms] = %v, want 2000", legacy.Payload["duration_ms"])
	}
}

// TestCodeResult_ToLegacyResult tests conversion to legacy Result
func TestCodeResult_ToLegacyResult(t *testing.T) {
	t.Parallel()
	ent := ecs.Entity{}

	r := &CodeResult{
		Ent:       ent,
		Err:       nil,
		Driver:    "slack",
		Color:     "red",
		Delivered: true,
	}

	legacy := r.ToLegacyResult()

	if legacy.Payload["type"] != "code" {
		t.Errorf("Payload[type] = %v, want code", legacy.Payload["type"])
	}
	if legacy.Payload["driver"] != "slack" {
		t.Errorf("Payload[driver] = %v, want slack", legacy.Payload["driver"])
	}
	if legacy.Payload["color"] != "red" {
		t.Errorf("Payload[color] = %v, want red", legacy.Payload["color"])
	}
	if legacy.Payload["delivered"] != true {
		t.Errorf("Payload[delivered] = %v, want true", legacy.Payload["delivered"])
	}
}

// TestNewPulseResult tests the factory function
func TestNewPulseResult(t *testing.T) {
	t.Parallel()
	ent := ecs.Entity{}
	testErr := errors.New("test")

	r := NewPulseResult(ent, testErr, "tcp")

	if r.Ent != ent {
		t.Error("Entity not set")
	}
	if r.Err != testErr {
		t.Error("Error not set")
	}
	if r.Driver != "tcp" {
		t.Errorf("Driver = %q, want %q", r.Driver, "tcp")
	}
}

// TestNewPulseResultWithLatency tests the factory function with latency
func TestNewPulseResultWithLatency(t *testing.T) {
	t.Parallel()
	ent := ecs.Entity{}
	latency := 50 * time.Millisecond

	r := NewPulseResultWithLatency(ent, nil, "icmp", latency)

	if r.Latency != latency {
		t.Errorf("Latency = %v, want %v", r.Latency, latency)
	}
}

// TestNewHTTPPulseResult tests the HTTP-specific factory function
func TestNewHTTPPulseResult(t *testing.T) {
	t.Parallel()
	ent := ecs.Entity{}

	r := NewHTTPPulseResult(ent, nil, 200, 100*time.Millisecond)

	if r.Driver != "http" {
		t.Errorf("Driver = %q, want %q", r.Driver, "http")
	}
	if r.StatusCode != 200 {
		t.Errorf("StatusCode = %d, want %d", r.StatusCode, 200)
	}
}

// TestNewInterventionResult tests the factory function
func TestNewInterventionResult(t *testing.T) {
	t.Parallel()
	ent := ecs.Entity{}

	r := NewInterventionResult(ent, nil, "docker", "stop")

	if r.Driver != "docker" {
		t.Errorf("Driver = %q, want %q", r.Driver, "docker")
	}
	if r.Action != "stop" {
		t.Errorf("Action = %q, want %q", r.Action, "stop")
	}
}

// TestNewDockerInterventionResult tests the Docker-specific factory function
func TestNewDockerInterventionResult(t *testing.T) {
	t.Parallel()
	ent := ecs.Entity{}

	r := NewDockerInterventionResult(ent, nil, "restart", "my-container")

	if r.Driver != "docker" {
		t.Errorf("Driver = %q, want %q", r.Driver, "docker")
	}
	if r.Action != "restart" {
		t.Errorf("Action = %q, want %q", r.Action, "restart")
	}
	if r.Container != "my-container" {
		t.Errorf("Container = %q, want %q", r.Container, "my-container")
	}
}

// TestNewCodeResult tests the factory function
func TestNewCodeResult(t *testing.T) {
	t.Parallel()
	ent := ecs.Entity{}

	r := NewCodeResult(ent, nil, "pagerduty", "yellow")

	if r.Driver != "pagerduty" {
		t.Errorf("Driver = %q, want %q", r.Driver, "pagerduty")
	}
	if r.Color != "yellow" {
		t.Errorf("Color = %q, want %q", r.Color, "yellow")
	}
	if r.Delivered {
		t.Error("Delivered should be false")
	}
}

// TestNewCodeResultDelivered tests the delivered factory function
func TestNewCodeResultDelivered(t *testing.T) {
	t.Parallel()
	ent := ecs.Entity{}

	r := NewCodeResultDelivered(ent, "email", "green")

	if r.Err != nil {
		t.Error("Error should be nil")
	}
	if !r.Delivered {
		t.Error("Delivered should be true")
	}
}

// TestFromLegacyResult_Pulse tests conversion from legacy Result to PulseResult
func TestFromLegacyResult_Pulse(t *testing.T) {
	t.Parallel()
	ent := ecs.Entity{}
	testErr := errors.New("test")

	legacy := Result{
		Ent: ent,
		Err: testErr,
		Payload: map[string]interface{}{
			"type":        "pulse",
			"driver":      "http",
			"status_code": 500,
			"latency_ms":  int64(100),
		},
	}

	jr := FromLegacyResult(legacy)
	if jr == nil {
		t.Fatal("FromLegacyResult returned nil")
	}

	pr, ok := jr.(*PulseResult)
	if !ok {
		t.Fatalf("Expected *PulseResult, got %T", jr)
	}

	if pr.Driver != "http" {
		t.Errorf("Driver = %q, want %q", pr.Driver, "http")
	}
	if pr.StatusCode != 500 {
		t.Errorf("StatusCode = %d, want %d", pr.StatusCode, 500)
	}
	if pr.Latency != 100*time.Millisecond {
		t.Errorf("Latency = %v, want %v", pr.Latency, 100*time.Millisecond)
	}
}

// TestFromLegacyResult_Intervention tests conversion from legacy Result
func TestFromLegacyResult_Intervention(t *testing.T) {
	t.Parallel()
	ent := ecs.Entity{}

	legacy := Result{
		Ent: ent,
		Payload: map[string]interface{}{
			"type":        "intervention",
			"driver":      "docker",
			"action":      "restart",
			"container":   "web",
			"duration_ms": int64(5000),
		},
	}

	jr := FromLegacyResult(legacy)
	if jr == nil {
		t.Fatal("FromLegacyResult returned nil")
	}

	ir, ok := jr.(*InterventionResult)
	if !ok {
		t.Fatalf("Expected *InterventionResult, got %T", jr)
	}

	if ir.Action != "restart" {
		t.Errorf("Action = %q, want %q", ir.Action, "restart")
	}
	if ir.Container != "web" {
		t.Errorf("Container = %q, want %q", ir.Container, "web")
	}
	if ir.Duration != 5*time.Second {
		t.Errorf("Duration = %v, want %v", ir.Duration, 5*time.Second)
	}
}

// TestFromLegacyResult_Code tests conversion from legacy Result
func TestFromLegacyResult_Code(t *testing.T) {
	t.Parallel()
	ent := ecs.Entity{}

	legacy := Result{
		Ent: ent,
		Payload: map[string]interface{}{
			"type":      "code",
			"driver":    "slack",
			"color":     "red",
			"delivered": true,
		},
	}

	jr := FromLegacyResult(legacy)
	if jr == nil {
		t.Fatal("FromLegacyResult returned nil")
	}

	cr, ok := jr.(*CodeResult)
	if !ok {
		t.Fatalf("Expected *CodeResult, got %T", jr)
	}

	if cr.Driver != "slack" {
		t.Errorf("Driver = %q, want %q", cr.Driver, "slack")
	}
	if cr.Color != "red" {
		t.Errorf("Color = %q, want %q", cr.Color, "red")
	}
	if !cr.Delivered {
		t.Error("Delivered should be true")
	}
}

// TestFromLegacyResult_NilPayload tests conversion with nil payload
func TestFromLegacyResult_NilPayload(t *testing.T) {
	t.Parallel()
	legacy := Result{Payload: nil}

	jr := FromLegacyResult(legacy)
	if jr != nil {
		t.Error("Expected nil for nil payload")
	}
}

// TestFromLegacyResult_UnknownType tests conversion with unknown type
func TestFromLegacyResult_UnknownType(t *testing.T) {
	t.Parallel()
	legacy := Result{
		Payload: map[string]interface{}{
			"type": "unknown",
		},
	}

	jr := FromLegacyResult(legacy)
	if jr != nil {
		t.Error("Expected nil for unknown type")
	}
}

// TestConvertJobResultToLegacy_Nil tests conversion with nil input
func TestConvertJobResultToLegacy_Nil(t *testing.T) {
	t.Parallel()
	legacy := ConvertJobResultToLegacy(nil)
	if legacy.Payload != nil {
		t.Error("Expected nil payload for nil input")
	}
}

// TestRoundTrip_PulseResult tests converting to legacy and back
func TestRoundTrip_PulseResult(t *testing.T) {
	t.Parallel()
	ent := ecs.Entity{}
	original := &PulseResult{
		Ent:        ent,
		Err:        nil,
		Driver:     "http",
		StatusCode: 200,
		Latency:    50 * time.Millisecond,
	}

	legacy := original.ToLegacyResult()
	roundtrip := FromLegacyResult(legacy).(*PulseResult)

	if roundtrip.Driver != original.Driver {
		t.Errorf("Driver mismatch: %q vs %q", roundtrip.Driver, original.Driver)
	}
	if roundtrip.StatusCode != original.StatusCode {
		t.Errorf("StatusCode mismatch: %d vs %d", roundtrip.StatusCode, original.StatusCode)
	}
	if roundtrip.Latency != original.Latency {
		t.Errorf("Latency mismatch: %v vs %v", roundtrip.Latency, original.Latency)
	}
}
