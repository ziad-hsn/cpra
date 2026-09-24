package jobs

import (
	"net/http"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"

	"github.com/ziad-hsn/cpra/internal/manifest"
)

func TestCreatePulseJobDefaultsHTTPMethod(t *testing.T) {
	pulse := manifest.Pulse{
		Type:    "http",
		Timeout: time.Second,
		Config:  &manifest.PulseHTTPConfig{Url: "https://example.com/health"},
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
