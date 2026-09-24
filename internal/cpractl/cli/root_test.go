package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func newTestCommand(t *testing.T, srvURL string, extraArgs ...string) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	root := NewRootCommand()
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs(append([]string{"--server", srvURL}, extraArgs...))
	return root, buf
}

func TestGetMonitorsUsesStableV2ResourcePage(t *testing.T) {
	var requests atomic.Int32
	fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/api/v2/monitors" || r.Method != "GET" || r.URL.Query().Get("limit") != "2" || r.URL.Query().Get("cursor") != "original-page" {
			t.Error("wrong monitor page request")
		}
		w.Header().Set("Content-Type", "application/json")
		m := cliResource("Monitor", "service-api")
		m.Metadata.Name = api.Pointer("alpha")
		m.Metadata.ResourceVersion = "revision-1"
		m.Status = json.RawMessage(`{"health":"down"}`)
		_ = json.NewEncoder(w).Encode(struct {
			Items      []api.Resource `json:"items"`
			NextCursor string         `json:"nextCursor"`
		}{[]api.Resource{m}, "next-page"})
	})
	for _, output := range []string{"table", "wide", "json", "yaml"} {
		stdout, stderr, err := fixture.run(t, nil, "get", "monitors", "--limit", "2", "--cursor", "original-page", "-o", output)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"service-api", "alpha", "revision-1"} {
			if !strings.Contains(stdout, want) {
				t.Fatalf("missing %s: %s", want, stdout)
			}
		}
		if !strings.Contains(stdout+stderr, "next-page") {
			t.Fatal("lost page cursor")
		}
	}
	if requests.Load() != 4 {
		t.Fatal("unexpected automatic pagination")
	}
}
func TestGetMonitorUsesStableStringID(t *testing.T) {
	fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/monitors/service-api" {
			t.Error("stable ID changed")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(cliResource("Monitor", "service-api"))
	})
	stdout, _, err := fixture.run(t, nil, "get", "monitor", "service-api", "-o", "json")
	if err != nil || !strings.Contains(stdout, `"id": "service-api"`) {
		t.Fatalf("stable monitor read: %v %s", err, stdout)
	}
}
func TestGetRejectsRemovedInputsWithoutRequests(t *testing.T) {
	var requests atomic.Int32
	fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) { requests.Add(1); t.Error("invalid command sent request") })
	for _, args := range [][]string{{"get", "overview"}, {"get", "state", "service-api"}, {"get", "monitors", "--status", "down"}, {"get", "monitors", "--type", "http"}, {"get", "monitors", "--code", "red"}, {"get", "monitors", "--query", "x"}, {"get", "monitors", "--page", "1"}, {"get", "monitors", "--size", "5"}, {"get", "monitor", "../invalid"}} {
		if _, _, err := fixture.run(t, nil, args...); err == nil {
			t.Fatalf("removed/invalid command accepted: %v", args)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("invalid command reached server")
	}
}
func TestHealthUsesV2Availability(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		available bool
		wantError bool
	}{{"live", 200, true, false}, {"not-reported", 200, false, true}, {"unhealthy", 503, false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v2/healthz" {
					t.Error("wrong health path")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(api.Health{Available: tc.available})
			})
			out, _, err := fixture.run(t, nil, "health")
			if (err != nil) != tc.wantError {
				t.Fatalf("health: %v", err)
			}
			if !tc.wantError && !strings.Contains(out, "ok") {
				t.Fatal("missing health success")
			}
		})
	}
}
func TestMetricsUsesBoundedSDKPrometheus(t *testing.T) {
	fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" || r.Method != "GET" || r.Header.Get("Authorization") != "Bearer named-operator-token" {
			t.Error("wrong metrics request")
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte("cpra_up 1\n"))
	})
	out, _, err := fixture.run(t, nil, "metrics")
	if err != nil || out != "cpra_up 1\n" {
		t.Fatalf("metrics: %v %q", err, out)
	}
}
