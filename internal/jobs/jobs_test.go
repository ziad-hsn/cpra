package jobs

import (
	"net/http"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"

	"cpra/internal/loader/schema"
)

func TestCreatePulseJobDefaultsHTTPMethod(t *testing.T) {
	pulse := schema.Pulse{
		Type:    "http",
		Timeout: time.Second,
		Config:  &schema.PulseHTTPConfig{Url: "https://example.com/health"},
	}

	job, err := CreatePulseJob(pulse, ecs.Entity{})
	if err != nil {
		t.Fatalf("CreatePulseJob returned error: %v", err)
	}

	httpJob, ok := job.(*PulseHTTPJob)
	if !ok {
		t.Fatalf("expected PulseHTTPJob, got %T", job)
	}

	if httpJob.Method != http.MethodGet {
		t.Fatalf("expected default method %q, got %q", http.MethodGet, httpJob.Method)
	}
}
