package jobs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/manifest"
)

func TestNotificationProviderColorAndCorrelation(t *testing.T) {
	for _, provider := range []string{"victorops", "datadog", "pushover"} {
		t.Run(provider, func(t *testing.T) {
			var mu sync.Mutex
			var bodies []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body := map[string]any{}
				if provider == "pushover" {
					if err := r.ParseForm(); err != nil {
						t.Error(err)
					}
					for k := range r.PostForm {
						body[k] = r.PostForm.Get(k)
					}
				} else if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				mu.Lock()
				bodies = append(bodies, body)
				mu.Unlock()
				w.WriteHeader(202)
				if provider == "pushover" {
					_, _ = w.Write([]byte(`{"status":1}`))
				}
			}))
			defer server.Close()
			for _, color := range []string{"yellow", "red", "cyan", "green", "gray"} {
				var config manifest.CodeNotification
				switch provider {
				case "victorops":
					config = &manifest.CodeNotificationVictorOps{URL: server.URL, EntityID: "shared"}
				case "datadog":
					config = &manifest.CodeNotificationDatadog{URL: server.URL, APIKey: "fixture"}
				case "pushover":
					config = &manifest.CodeNotificationPushover{URL: server.URL, AppToken: "fixture", UserKey: "fixture", Priority: 2}
				}
				job, err := CreateCodeJob("service", manifest.CodeConfig{Notify: provider, Config: config}, ecs.Entity{}, color)
				if err != nil {
					t.Fatal(err)
				}
				dispatch := NewDispatch(job, ecs.Entity{}, "code", color, 0, 0)
				dispatch.MonitorID, dispatch.FileIncarnation, dispatch.NotificationCorrelation, dispatch.EndpointID = "service", "incarnation", "episode", "endpoint"
				if result := dispatch.Execute(); result.Err != nil {
					t.Fatal(result.Err)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if len(bodies) != 5 {
				t.Fatal("wrong invocation count", len(bodies))
			}
			switch provider {
			case "victorops":
				for i, kind := range []string{"WARNING", "CRITICAL", "INFO", "RECOVERY", "INFO"} {
					if bodies[i]["message_type"] != kind || bodies[i]["entity_id"] != bodies[0]["entity_id"] || bodies[i]["entity_id"] == "shared" {
						t.Fatal("incorrect incident mapping", bodies)
					}
				}
			case "datadog":
				for i, kind := range []string{"warning", "error", "info", "success", "info"} {
					if bodies[i]["alert_type"] != kind {
						t.Fatal("incorrect severity", bodies)
					}
				}
			case "pushover":
				for i := range bodies {
					if i < 3 && bodies[i]["priority"] != "2" || i >= 3 && (bodies[i]["priority"] != nil || bodies[i]["retry"] != nil || bodies[i]["expire"] != nil) {
						t.Fatal("informational event used shared emergency priority", bodies)
					}
				}
			}
		})
	}
}

func TestPushoverExplicitColorPriority(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("priority") != "2" || r.Form.Get("retry") != "60" || r.Form.Get("expire") != "1800" {
			t.Error("per-color emergency missing")
		}
		w.WriteHeader(202)
		_, _ = w.Write([]byte(`{"status":1}`))
	}))
	defer server.Close()
	job, err := newCodePushoverJob(&manifest.CodeNotificationPushover{URL: server.URL, PriorityByColor: map[string]int{"green": 2}}, "service", "green", "Recovered", ecs.Entity{})
	if err != nil {
		t.Fatal(err)
	}
	if r := job.Execute(); r.Err != nil {
		t.Fatal(r.Err)
	}
	if calls != 1 {
		t.Fatal("unexpected request count", calls)
	}
}

func TestNotificationWebhookCommittedIdentityAndTimestamp(t *testing.T) {
	var bodies []notificationEnvelope
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body notificationEnvelope
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		bodies = append(bodies, body)
		w.WriteHeader(204)
	}))
	defer server.Close()
	prototype := &CodeWebhookJob{URL: server.URL, Monitor: "Service", Color: "red", Message: "Outage"}
	at := time.Date(2026, 10, 16, 20, 0, 0, 0, time.FixedZone("fixture", 7200))
	for attempt := 1; attempt <= 2; attempt++ {
		d := NewDispatch(prototype, ecs.Entity{}, "code", "red", 0, 0)
		d.MonitorID, d.FileIncarnation, d.IncidentID, d.NotificationCorrelation = "monitor", "incarnation", "incident", "episode"
		d.ActionID, d.DeliveryID, d.EndpointID, d.NotificationAt, d.Attempt = "action", "delivery", "endpoint", at, attempt
		if r := d.Execute(); r.Err != nil {
			t.Fatal(r.Err)
		}
	}
	if len(bodies) != 2 || bodies[0] != bodies[1] || bodies[0].DeliveryID != "delivery" || bodies[0].IncarnationID != "incarnation" || !bodies[0].At.Equal(at) || bodies[0].At.Location() != time.UTC {
		t.Fatal("retry identity or event time changed", bodies)
	}
	if prototype.notification.MonitorID != "" {
		t.Fatal("dispatch mutated reusable template")
	}
	yellow := buildCodeNotificationMessage("Service", codeAlertTemplateFor("yellow"))
	if strings.Contains(yellow, "beyond safe thresholds") || strings.Contains(yellow, "threshold may") {
		t.Fatal("yellow notification guesses threshold state")
	}
	if strings.Contains(buildCodeNotificationMessage("Service", codeAlertTemplateFor("red")), "Perform manual recovery") {
		t.Fatal("red notification must not prescribe an intervention outside its policy")
	}
}
