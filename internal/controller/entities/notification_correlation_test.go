package entities

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/manifest"
)

func TestPreparedNotificationColorsCloseOriginalProviderIncident(t *testing.T) {
	var mu sync.Mutex
	var received []map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		received = append(received, body)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer server.Close()
	m := testMonitor()
	m.Codes = manifest.Codes{
		"red":   {Dispatch: true, Notify: "victorops", Config: &manifest.CodeNotificationVictorOps{URL: server.URL, EntityID: "api", MessageType: "CRITICAL"}},
		"green": {Dispatch: true, Notify: "victorops", Config: &manifest.CodeNotificationVictorOps{URL: server.URL, EntityID: "api", MessageType: "RECOVERY"}},
	}
	p := prepareTestMonitor(t, m)
	if p.NotificationEndpointIDs("red")[0] == p.NotificationEndpointIDs("green")[0] {
		t.Fatal("fixture did not exercise distinct immutable delivery targets")
	}
	for _, episode := range []string{"outage-one", "outage-two"} {
		for _, color := range []string{"red", "green"} {
			d := jobs.NewDispatch(p.bundle.jobs.CodeJobs[color][0], ecs.Entity{}, "code", color, 1, 0)
			d.MonitorID, d.FileIncarnation, d.NotificationCorrelation = m.ID, "incarnation-one", episode
			d.EndpointID = p.NotificationEndpointIDs(color)[0]
			if result := d.Execute(); result.Err != nil {
				t.Fatal(result.Err)
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(received) != 4 || received[0]["message_type"] != "CRITICAL" || received[1]["message_type"] != "RECOVERY" {
		t.Fatal("provider did not receive trigger and recovery")
	}
	if received[0]["entity_id"] == "" || received[0]["entity_id"] != received[1]["entity_id"] || received[2]["entity_id"] != received[3]["entity_id"] || received[0]["entity_id"] == received[2]["entity_id"] {
		t.Fatal("provider correlation did not preserve the episode boundary")
	}
}
