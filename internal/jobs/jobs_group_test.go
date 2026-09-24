package jobs

import (
	"testing"

	"github.com/ziad-hsn/cpra/internal/manifest"

	"github.com/mlange-42/ark/ecs"
)

func TestCreateCodeJobsGroupResolution(t *testing.T) {
	w := ecs.NewWorld()
	ent := w.NewEntity()

	endpoints := map[string]manifest.Endpoint{
		"ops_email":   {Type: "email", Config: &manifest.CodeNotificationEmail{To: "ops@example.com", From: "cpra@example.com", Server: "localhost:25"}},
		"status_hook": {Type: "webhook", Config: &manifest.CodeNotificationWebhook{URL: "https://hooks.example.com"}},
	}
	groups := manifest.NotificationGroups{
		"oncall": {"ops_email", "status_hook"},
	}

	// Group reference resolves to one job per endpoint.
	cfg := manifest.CodeConfig{Dispatch: true, NotifyGroup: "oncall"}
	got, err := CreateCodeJobs("monitor", cfg, ent, "red", endpoints, groups)
	if err != nil {
		t.Fatalf("CreateCodeJobs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 jobs for group, got %d", len(got))
	}

	// Inline config returns a single job.
	inline := manifest.CodeConfig{Dispatch: true, Notify: "log", Config: &manifest.CodeNotificationLog{File: "x.log"}}
	got2, err := CreateCodeJobs("monitor", inline, ent, "red", endpoints, groups)
	if err != nil {
		t.Fatalf("CreateCodeJobs inline: %v", err)
	}
	if len(got2) != 1 {
		t.Fatalf("expected 1 job for inline, got %d", len(got2))
	}

	// Unknown group errors.
	bad := manifest.CodeConfig{Dispatch: true, NotifyGroup: "missing"}
	if _, err := CreateCodeJobs("monitor", bad, ent, "red", endpoints, groups); err == nil {
		t.Fatal("expected error for unknown group")
	}

	// Group referencing a missing endpoint errors.
	groups2 := manifest.NotificationGroups{"broken": {"nope"}}
	bad2 := manifest.CodeConfig{Dispatch: true, NotifyGroup: "broken"}
	if _, err := CreateCodeJobs("monitor", bad2, ent, "red", endpoints, groups2); err == nil {
		t.Fatal("expected error for missing endpoint")
	}
}
