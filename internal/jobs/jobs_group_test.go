package jobs

import (
	"testing"

	"cpra/internal/loader/schema"

	"github.com/mlange-42/ark/ecs"
)

func TestCreateCodeJobsGroupResolution(t *testing.T) {
	w := ecs.NewWorld()
	ent := w.NewEntity()

	endpoints := map[string]schema.Endpoint{
		"ops_email":   {Type: "email", Config: &schema.CodeNotificationEmail{To: "ops@example.com", From: "cpra@example.com", Server: "localhost:25"}},
		"status_hook": {Type: "webhook", Config: &schema.CodeNotificationWebhook{URL: "https://hooks.example.com"}},
	}
	groups := schema.NotificationGroups{
		"oncall": {"ops_email", "status_hook"},
	}

	// Group reference resolves to one job per endpoint.
	cfg := schema.CodeConfig{Dispatch: true, NotifyGroup: "oncall"}
	got, err := CreateCodeJobs("monitor", cfg, ent, "red", endpoints, groups)
	if err != nil {
		t.Fatalf("CreateCodeJobs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 jobs for group, got %d", len(got))
	}

	// Inline config returns a single job.
	inline := schema.CodeConfig{Dispatch: true, Notify: "log", Config: &schema.CodeNotificationLog{File: "x.log"}}
	got2, err := CreateCodeJobs("monitor", inline, ent, "red", endpoints, groups)
	if err != nil {
		t.Fatalf("CreateCodeJobs inline: %v", err)
	}
	if len(got2) != 1 {
		t.Fatalf("expected 1 job for inline, got %d", len(got2))
	}

	// Unknown group errors.
	bad := schema.CodeConfig{Dispatch: true, NotifyGroup: "missing"}
	if _, err := CreateCodeJobs("monitor", bad, ent, "red", endpoints, groups); err == nil {
		t.Fatal("expected error for unknown group")
	}

	// Group referencing a missing endpoint errors.
	groups2 := schema.NotificationGroups{"broken": {"nope"}}
	bad2 := schema.CodeConfig{Dispatch: true, NotifyGroup: "broken"}
	if _, err := CreateCodeJobs("monitor", bad2, ent, "red", endpoints, groups2); err == nil {
		t.Fatal("expected error for missing endpoint")
	}
}
