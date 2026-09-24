package entities

import (
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/manifest"

	"github.com/mlange-42/ark/ecs"
)

func TestCreateEntityFromMonitorFanOut(t *testing.T) {
	w := ecs.NewWorld()
	mgr := NewEntityManager(&w)

	mgr.Endpoints = map[string]manifest.Endpoint{
		"ops_email":   {Type: "email", Config: &manifest.CodeNotificationEmail{To: "ops@example.com", From: "cpra@example.com", Server: "localhost:25"}},
		"status_hook": {Type: "webhook", Config: &manifest.CodeNotificationWebhook{URL: "https://hooks.example.com"}},
	}
	mgr.NotificationGroups = manifest.NotificationGroups{
		"oncall": {"ops_email", "status_hook"},
	}

	monitor := &manifest.Monitor{
		Name:    "api",
		Enabled: true,
		Pulse: manifest.Pulse{
			Type:     "http",
			Interval: time.Second,
			Timeout:  time.Second,
			Config:   &manifest.PulseHTTPConfig{Url: "https://api.example.com/health"},
		},
		Codes: manifest.Codes{
			"red": {Dispatch: true, NotifyGroup: "oncall"},
		},
	}

	if err := mgr.CreateEntityFromMonitor(monitor, &w); err != nil {
		t.Fatalf("CreateEntityFromMonitor: %v", err)
	}

	query := ecs.NewFilter1[components.JobStorage](&w).Query()
	count := 0
	for query.Next() {
		js := query.Get()
		jobs := js.CodeJobs["red"]
		if len(jobs) != 2 {
			t.Fatalf("expected 2 jobs for red, got %d", len(jobs))
		}
		count++
	}
	if count != 1 {
		t.Fatalf("expected 1 entity with JobStorage, got %d", count)
	}
}

func TestCreateEntityFromMonitorInlineSingleJob(t *testing.T) {
	w := ecs.NewWorld()
	mgr := NewEntityManager(&w)

	monitor := &manifest.Monitor{
		Name:    "api",
		Enabled: true,
		Pulse: manifest.Pulse{
			Type:     "http",
			Interval: time.Second,
			Timeout:  time.Second,
			Config:   &manifest.PulseHTTPConfig{Url: "https://api.example.com/health"},
		},
		Codes: manifest.Codes{
			"red": {Dispatch: true, Notify: "log", Config: &manifest.CodeNotificationLog{File: "x.log"}},
		},
	}

	if err := mgr.CreateEntityFromMonitor(monitor, &w); err != nil {
		t.Fatalf("CreateEntityFromMonitor: %v", err)
	}

	query := ecs.NewFilter1[components.JobStorage](&w).Query()
	count := 0
	for query.Next() {
		js := query.Get()
		jobs := js.CodeJobs["red"]
		if len(jobs) != 1 {
			t.Fatalf("expected 1 job for inline red, got %d", len(jobs))
		}
		count++
	}
	if count != 1 {
		t.Fatalf("expected 1 entity with JobStorage, got %d", count)
	}
}
