package persistence

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/jobs"
)

func TestProviderCorrelationSurvivesFormatUpgrade(t *testing.T) {
	for _, driver := range []string{"pagerduty", "victorops"} {
		t.Run(driver, func(t *testing.T) {
			var identities []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				field := "dedup_key"
				if driver == "victorops" {
					field = "entity_id"
				}
				identity, _ := body[field].(string)
				identities = append(identities, identity)
				w.WriteHeader(202)
			}))
			defer server.Close()
			config := testMonitor()
			config.Policy = lifecyclePolicy()
			config.Policy.Healthy = 1
			config.Policy.NotificationEndpointIDs = map[string][]string{"red": {"endpoint"}, "green": {"endpoint"}}
			at := policyTime()
			m := *transitionAtFormat(Monitor{}, Command{Kind: "configure", MonitorID: config.ID, Revision: config.Revision, Config: &config, At: at}, DeliveryFormatVersion).Monitor
			m = *transitionAtFormat(m, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, Outcome: "failure", At: at.Add(time.Second)}, DeliveryFormatVersion).Monitor
			red := notificationByColor(t, m, "red", Queued)
			invoke := func(a Action) {
				var job jobs.Job = &jobs.CodePagerDutyJob{URL: server.URL, RoutingKey: "fixture", Color: a.Color}
				if driver == "victorops" {
					job = &jobs.CodeVictorOpsJob{URL: server.URL, EntityID: "configured-original", Color: a.Color}
				}
				d := jobs.NewDispatch(job, ecs.Entity{}, "code", a.Color, 1, 0)
				d.MonitorID, d.CatalogUID, d.NotificationCorrelation, d.EndpointID = m.ID, m.CatalogUID, a.NotificationCorrelation, a.EndpointID
				d.ProviderCorrelationVersion = a.ProviderCorrelationVersion
				if r := d.Execute(); r.Err != nil {
					t.Fatal(r.Err)
				}
			}
			m = *transitionAtFormat(m, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: red.ID, At: at.Add(2 * time.Second)}, DeliveryFormatVersion).Monitor
			invoke(m.Actions[red.ID])
			m = *transitionAtFormat(m, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: red.ID, Outcome: "success", At: at.Add(3 * time.Second)}, DeliveryFormatVersion).Monitor
			raw, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			var restored Monitor
			if err := json.Unmarshal(raw, &restored); err != nil {
				t.Fatal(err)
			}
			if err := validateNotificationIdentity(restored, DeliveryFormatVersion); err != nil {
				t.Fatal(err)
			}
			m = policyPulse(t, restored, at.Add(4*time.Second), "success")
			green := notificationByColor(t, m, "green", Queued)
			if green.ProviderCorrelationVersion != 0 {
				t.Fatal("upgrade changed an exposed episode's correlation")
			}
			invoke(green)
			if len(identities) != 2 || identities[0] == "" || identities[0] != identities[1] {
				t.Fatal("recovery orphaned the pre-upgrade provider incident")
			}
			m = policyPulse(t, m, at.Add(5*time.Second), "failure")
			if next := notificationByColor(t, m, "red", Queued); next.ProviderCorrelationVersion != 1 {
				t.Fatal("new incident did not adopt destination correlation")
			}
		})
	}
}

func TestProviderCorrelationUpgradeRetainsRejectedAttempt(t *testing.T) {
	config := testMonitor()
	config.Policy = lifecyclePolicy()
	config.Policy.Healthy = 1
	at := policyTime()
	m := *transitionAtFormat(Monitor{}, Command{Kind: "configure", MonitorID: config.ID, Revision: config.Revision, Config: &config, At: at}, DeliveryFormatVersion).Monitor
	m = *transitionAtFormat(m, Command{Kind: "pulse", MonitorID: m.ID, Revision: m.Revision, Generation: 1, Outcome: "failure", At: at.Add(time.Second)}, DeliveryFormatVersion).Monitor
	red := notificationByColor(t, m, "red", Queued)
	m = *transitionAtFormat(m, Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: red.ID, At: at.Add(2 * time.Second)}, DeliveryFormatVersion).Monitor
	m = *transitionAtFormat(m, Command{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: red.ID, Outcome: "failure", Retryable: true, At: at.Add(3 * time.Second)}, DeliveryFormatVersion).Monitor
	retry := notificationByColor(t, m, "red", Queued)
	if retry.Attempt != 2 || !retry.StartedAt.IsZero() {
		t.Fatal("fixture did not retain the historical retry state")
	}
	m = policyPulse(t, m, at.Add(4*time.Second), "success")
	green := notificationByColor(t, m, "green", Queued)
	if green.ProviderCorrelationVersion != 0 || m.NotificationAttempted != red.NotificationCorrelation || green.NotificationCorrelation != red.NotificationCorrelation {
		t.Fatal("upgrade discarded a committed prior start or changed its remote incident")
	}
	if m.Actions[retry.ID].State != Cancelled {
		t.Fatal("recovery left the obsolete retry queued")
	}
}
