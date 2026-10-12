package jobs

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/manifest"
)

func TestNotificationServerErrorDoesNotProveRejection(t *testing.T) {
	for _, status := range []int{500, 502, 503, 504} {
		for _, driver := range []string{"slack", "discord", "pagerduty", "webhook", "opsgenie"} {
			t.Run(http.StatusText(status)+"/"+driver, func(t *testing.T) {
				var effects atomic.Int32
				receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					effects.Add(1) // The effect happens before the failing response.
					w.WriteHeader(status)
					_, _ = w.Write([]byte("provider-secret-response"))
				}))
				defer receiver.Close()
				var config manifest.CodeNotification
				switch driver {
				case "slack":
					config = &manifest.CodeNotificationSlack{WebHook: receiver.URL}
				case "discord":
					config = &manifest.CodeNotificationDiscord{WebhookURL: receiver.URL}
				case "pagerduty":
					config = &manifest.CodeNotificationPagerDuty{URL: receiver.URL, RoutingKey: "fixture"}
				case "webhook":
					config = &manifest.CodeNotificationWebhook{URL: receiver.URL}
				case "opsgenie":
					config = &manifest.CodeNotificationOpsgenie{URL: receiver.URL, APIKey: "fixture"}
				}
				job, err := CreateCodeJob("fixture", manifest.CodeConfig{Notify: driver, Config: config}, ecs.Entity{}, "red")
				if err != nil {
					t.Fatal(err)
				}
				result := job.Execute()
				var rejection *DeliveryError
				if result.Err == nil || errors.As(result.Err, &rejection) {
					t.Fatalf("a remote effect followed by %d was classified as definite rejection: %v", status, result.Err)
				}
				if effects.Load() != 1 || strings.Contains(result.Err.Error(), "provider-secret-response") {
					t.Fatal("transport replayed an effect or exposed its response body")
				}
			})
		}
	}
}

func TestInterventionNotificationDoesNotClaimVerifiedHealth(t *testing.T) {
	var message string
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		message = payload["message"]
		w.WriteHeader(http.StatusAccepted)
	}))
	defer receiver.Close()
	for _, color := range []string{"cyan", "red"} {
		job, err := CreateCodeJob("fixture", manifest.CodeConfig{Notify: "webhook", Config: &manifest.CodeNotificationWebhook{URL: receiver.URL}}, ecs.Entity{}, color)
		if err != nil {
			t.Fatal(err)
		}
		if result := job.Execute(); result.Err != nil {
			t.Fatal(result.Err)
		}
		if color == "cyan" && (strings.Contains(message, "RESTORED") || strings.Contains(message, "health checks are passing") || !strings.Contains(message, "health verification pending")) {
			t.Fatalf("intervention completion claimed health before verification: %s", message)
		}
		if color == "red" && strings.Contains(message, "failures and interventions") {
			t.Fatal("outage notification claimed an intervention was configured or attempted")
		}
	}
}
