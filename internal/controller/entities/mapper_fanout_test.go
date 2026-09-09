package entities

import (
	"testing"
	"time"

	"cpra/internal/controller/components"
	"cpra/internal/loader/schema"

	"github.com/mlange-42/ark/ecs"
)

func TestCreateEntityFromMonitorFanOut(t *testing.T) {
	w := ecs.NewWorld()
	mgr := NewEntityManager(&w)

	mgr.Endpoints = map[string]schema.Endpoint{
		"ops_email":   {Type: "email", Config: &schema.CodeNotificationEmail{To: "ops@example.com", From: "cpra@example.com", Server: "localhost:25"}},
		"status_hook": {Type: "webhook", Config: &schema.CodeNotificationWebhook{URL: "https://hooks.example.com"}},
	}
	mgr.NotificationGroups = schema.NotificationGroups{
		"oncall": {"ops_email", "status_hook"},
	}

	monitor := &schema.Monitor{
		Name:    "api",
		Enabled: true,
		Pulse: schema.Pulse{
			Type:     "http",
			Interval: time.Second,
			Timeout:  time.Second,
			Config:   &schema.PulseHTTPConfig{Url: "https://api.example.com/health"},
		},
		Codes: schema.Codes{
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

	monitor := &schema.Monitor{
		Name:    "api",
		Enabled: true,
		Pulse: schema.Pulse{
			Type:     "http",
			Interval: time.Second,
			Timeout:  time.Second,
			Config:   &schema.PulseHTTPConfig{Url: "https://api.example.com/health"},
		},
		Codes: schema.Codes{
			"red": {Dispatch: true, Notify: "log", Config: &schema.CodeNotificationLog{File: "x.log"}},
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
