package cli

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
	"gopkg.in/yaml.v3"
)

func TestRuntimeObservationsUseV2PagesAndAvailability(t *testing.T) {
	for _, tc := range []struct {
		name string
		page any
		want []string
	}{
		{"queues", api.QueueList{Available: true, Items: []api.Queue{{Name: "pulse", Available: true, Depth: 5, Capacity: 30, EnqueuedPerSecond: 1.5, DequeuedPerSecond: 1.4, RatesAvailable: true, Drops: 2}}, NextCursor: "next-page"}, []string{"pulse", "5", "1.500", "unavailable"}},
		{"pools", api.PoolList{Available: true, Items: []api.Pool{{Name: "http", Available: true, Workers: 8, Busy: 99, BusyAvailable: false, Target: 8, Capacity: 100}}, NextCursor: "next-page"}, []string{"http", "8", "unavailable"}},
		{"systems", api.SystemList{Available: true, Items: []api.System{{Name: "check", Available: true, Updates: 8, LastUpdateDurationMS: api.Measurement{Available: true}}}, NextCursor: "next-page"}, []string{"check", "8", "0.000", "unavailable"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != "GET" || r.URL.Path != "/api/v2/"+tc.name || r.URL.Query().Get("limit") != "2" || r.URL.Query().Get("cursor") != "original" {
					t.Error("observation lost bounded v2 route")
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(tc.page)
			})
			for _, format := range []string{"table", "wide", "json", "yaml"} {
				out, _, err := fixture.run(t, nil, "get", tc.name, "--limit", "2", "--cursor", "original", "-o", format)
				if err != nil {
					t.Fatal(err)
				}
				if format == "table" || format == "wide" {
					for _, want := range append(tc.want, "next-page") {
						if !strings.Contains(out, want) {
							t.Fatalf("missing %s: %s", want, out)
						}
					}
					if tc.name == "pools" && strings.Contains(out, "99") {
						t.Fatal("unavailable busy observation rendered as99")
					}
				} else {
					assertObservationEncoding(t, out, format, tc.page)
				}
			}
			if requests.Load() != 4 {
				t.Fatal("observation eagerly fetched additional pages")
			}
		})
	}
}

func assertObservationEncoding(t *testing.T, out, format string, want any) {
	t.Helper()
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var expected, actual any
	if err = json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	if format == "yaml" {
		var value any
		if err = yaml.Unmarshal([]byte(out), &value); err != nil {
			t.Fatal(err)
		}
		raw, err = json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
	} else {
		raw = []byte(out)
	}
	if err = json.Unmarshal(raw, &actual); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("%s changed wire fields: %s", format, out)
	}
}

func TestStateAndConfigHonorAvailability(t *testing.T) {
	for _, tc := range []struct {
		name   string
		value  any
		hidden string
	}{
		{"state", api.State{GeneratedAt: time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC), Live: true, Ready: false, UnknownActions: 999, UnknownActionsAvailable: false, Storage: api.StorageState{Available: true, Mode: "raft", AppliedIndex: 42, Bytes: 999, BytesAvailable: false}}, "999"},
		{"config", api.RuntimeConfig{Available: false, QueueCapacity: 999, StorageMode: "hidden", ReadOnly: true}, "hidden"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v2/"+tc.name {
					t.Error("wrong runtime endpoint")
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(tc.value)
			})
			out, _, err := fixture.run(t, nil, "get", tc.name)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, "unavailable") || strings.Contains(out, tc.hidden) {
				t.Fatalf("unavailable observations printed as data: %s", out)
			}
			for _, format := range []string{"json", "yaml"} {
				out, _, err := fixture.run(t, nil, "get", tc.name, "-o", format)
				if err != nil {
					t.Fatal(err)
				}
				assertObservationEncoding(t, out, format, tc.value)
			}
		})
	}
}

func TestHistoryAndIncidentsUseCurrentResourceReads(t *testing.T) {
	var requests atomic.Int32
	fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v2/history":
			if r.URL.Query().Get("monitorID") != "service-api" || r.URL.Query().Get("limit") != "3" || r.URL.Query().Get("cursor") != "original" {
				t.Error("history lost original selector")
			}
			_ = json.NewEncoder(w).Encode(api.EventList{Items: []api.Event{{ID: "event", MonitorID: "service-api", Kind: "incident_opened"}}, NextCursor: "more"})
		case "/api/v2/incidents":
			if r.URL.Query().Get("monitorID") != "service-api" {
				t.Error("incident lost monitor selector")
			}
			_ = json.NewEncoder(w).Encode(api.IncidentList{Items: []api.Incident{{ID: "incident", MonitorID: "service-api", Revision: "revision-1", State: "open"}}})
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
			http.NotFound(w, r)
		}
	})
	for _, args := range [][]string{{"get", "history", "service-api", "--limit", "3", "--cursor", "original", "-o", "json"}, {"get", "incidents", "--monitor-id", "service-api", "-o", "json"}} {
		if _, _, err := fixture.run(t, nil, args...); err != nil {
			t.Fatal(err)
		}
	}
	if requests.Load() != 2 {
		t.Fatal("navigation issued extra reads")
	}
}

func TestObservationsNeverFallbackToV1(t *testing.T) {
	for _, args := range [][]string{{"get", "monitors"}, {"get", "incidents"}, {"get", "queues"}, {"get", "pools"}, {"get", "systems"}, {"get", "config"}, {"get", "state"}, {"get", "slo"}, {"get", "history", "service-api"}, {"health"}, {"ready"}} {
		t.Run(strings.Join(args, "-"), func(t *testing.T) {
			var requests atomic.Int32
			fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if !strings.HasPrefix(r.URL.Path, "/api/v2/") {
					t.Error("compatibility fallback used")
				}
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(http.StatusNotFound)
				_ = json.NewEncoder(w).Encode(api.Problem{Status: 404, Detail: cliSecretValue})
			})
			out, stderr, err := fixture.run(t, nil, args...)
			if err == nil || requests.Load() != 1 {
				t.Fatalf("unsupported route retried: %v (%d)", err, requests.Load())
			}
			if strings.Contains(out+stderr+err.Error(), cliSecretValue) {
				t.Fatal("provider error leaked")
			}
		})
	}
}
