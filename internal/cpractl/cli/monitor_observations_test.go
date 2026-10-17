package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestMonitorObservationCLIUsesAccessModeWithoutWritableResources(t *testing.T) {
	item := api.MonitorObservation{MonitorID: "api", Name: "API", IncarnationUID: "instance", Source: api.File, Health: api.Unhealthy, Enabled: true, Ignored: true, PendingNotifications: 3, ConfiguredNotifications: []string{}}
	calls := 0
	f := newCLIAPIFixture(t, api.AccessDiscoveryModeObservation, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || !strings.HasPrefix(r.URL.Path, "/api/v2/observations/monitors") {
			t.Error("observation read attempted management", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/api") {
			_ = json.NewEncoder(w).Encode(item)
		} else {
			_ = json.NewEncoder(w).Encode(api.MonitorObservationList{Items: []api.MonitorObservation{item}, NextCursor: "next-page"})
		}
	})
	stdout, stderr, err := f.run(t, nil, "get", "monitors")
	if err != nil || !strings.Contains(stdout, "OBSERVED HEALTH") || !strings.Contains(stdout, "unhealthy") || !strings.Contains(stderr, "next-page") || calls != 1 {
		t.Fatal(stdout, stderr, calls, err)
	}
	stdout, _, err = f.run(t, nil, "describe", "monitor/api")
	if err != nil || !strings.Contains(stdout, "read-only") || !strings.Contains(stdout, "Unavailable") || strings.Contains(stdout, "0001") {
		t.Fatal(stdout, err)
	}
	stdout, _, err = f.run(t, nil, "get", "monitor/api", "-o", "json")
	if err != nil || !strings.Contains(stdout, "incarnationUID") || strings.Contains(stdout, "resourceVersion") {
		t.Fatal(stdout, err)
	}
	for _, alias := range []string{"monitor", "monitors", "mon", "mons"} {
		if _, _, err := f.run(t, nil, "get", alias); err != nil {
			t.Fatal("supported observation alias", alias, err)
		}
	}
	for _, alias := range []string{"monitor-configurations", "monitor-configuration", "configurations"} {
		if _, _, err := f.run(t, nil, "get", alias); err == nil {
			t.Fatal("file mode fabricated configuration", alias)
		}
	}
}

func TestMonitorManagementReadFailureNeverDowngrades(t *testing.T) {
	for _, code := range []int{401, 403, 404} {
		calls := 0
		f := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
			calls++
			if r.URL.Path != "/api/v2/monitors/api" {
				t.Error("management failure fell back", r.URL.Path)
			}
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(api.Problem{Status: int64(code), Code: "unavailable"})
		})
		if _, _, err := f.run(t, nil, "get", "monitor/api"); err == nil || calls != 1 {
			t.Fatal(code, calls, err)
		}
	}
}
