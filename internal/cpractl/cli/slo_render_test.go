package cli

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestSLOPauseAvailabilityAndSeparateMisses(t *testing.T) {
	for _, tc := range []struct {
		name      string
		available bool
		paused    int64
		seconds   float64
		want      string
	}{{"unavailable", false, 0, 0, "unavailable"}, {"reported-zero", true, 0, 0, "0.000"}, {"reported-pause", true, 2, 15.5, "15.500"}} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v2/slo" || r.Method != "GET" {
					t.Error("wrong SLO request")
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(api.SLOView{Available: tc.available, GeneratedAt: time.Now(), Window: "5m", QueueTargetMS: 250, ResultTargetMS: 5000, Reports: []api.SLOReport{{Driver: "http", Pipeline: "check", Missed: 3, PausedMonitors: tc.paused, PausedMonitorSeconds: tc.seconds, Condition: "insufficient_samples", QueueAttainment: api.Measurement{Available: true}, ResultAttainment: api.Measurement{Available: true}}}})
			})
			out, _, err := fixture.run(t, nil, "get", "slo")
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"PAUSED", "PAUSE MONITOR-SECONDS", "MISSED", tc.want} {
				if !strings.Contains(out, want) {
					t.Fatalf("missing %s: %s", want, out)
				}
			}
			if tc.available && (!strings.Contains(out, "3") || !strings.Contains(out, "0.000%")) {
				t.Fatal("lost miss count or measured zero")
			}
		})
	}
}
func TestUnavailableSLOMeasurementsDoNotBecomeZero(t *testing.T) {
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -1} {
		m := api.Measurement{Available: true, Value: v}
		if measurement(m) != "unavailable" || measurementPercent(m) != "unavailable" {
			t.Fatal("invalid measurement displayed")
		}
	}
	if measurement(api.Measurement{}) != "unavailable" || measurement(api.Measurement{Available: true}) != "0.000" || measurementPercent(api.Measurement{Available: true}) != "0.000%" {
		t.Fatal("unavailable and measured zero conflated")
	}
}
