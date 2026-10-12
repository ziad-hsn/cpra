package jobs

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mlange-42/ark/ecs"
)

func TestPagerDutyIncidentEndpointCorrelationAndSeverity(t *testing.T) {
	var received []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var data map[string]any
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			t.Error(err)
		}
		received = append(received, data)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	for _, color := range []string{"yellow", "red", "red", "green"} {
		job := &CodePagerDutyJob{URL: server.URL, RoutingKey: "fixture", Monitor: "service", Message: "safe summary", Color: color}
		dispatch := NewDispatch(job, ecs.Entity{}, "code", color, 0, 0)
		dispatch.MonitorID, dispatch.CatalogUID, dispatch.IncidentID, dispatch.EndpointID = "service", "incarnation", "incident", "endpoint"
		if result := dispatch.Execute(); result.Err != nil {
			t.Fatal(result.Err)
		}
	}
	key := received[0]["dedup_key"]
	for _, data := range received {
		if data["dedup_key"] != key {
			t.Fatal("trigger, retry or resolve changed incident identity")
		}
	}
	if received[0]["payload"].(map[string]any)["severity"] != "warning" || received[1]["payload"].(map[string]any)["severity"] != "critical" || received[3]["event_action"] != "resolve" {
		t.Fatal("incorrect severity or closure")
	}
	first := &CodePagerDutyJob{}
	bindNotificationContext(first, &Dispatch{Kind: "code", MonitorID: "service", CatalogUID: "incarnation", IncidentID: "another", EndpointID: "endpoint"})
	if first.CorrelationKey == key {
		t.Fatal("distinct incident collapsed into the previous alert")
	}
}

func TestOpsgenieCorrelatesCreateCloseAndConfirmsProcessing(t *testing.T) {
	const requestID = "11111111-1111-4111-8111-111111111111"
	var paths []string
	var alias string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.RequestURI())
		if r.Header.Get("Authorization") != "GenieKey fixture" {
			t.Error("missing scoped authentication")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"data":{"success":true,"isSuccess":true}}`))
			return
		}
		var data map[string]string
		if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
			t.Error(err)
		}
		if r.URL.Path == "/v2/alerts" {
			alias = data["alias"]
			if data["priority"] != "P3" {
				t.Error("warning priority was lost")
			}
		} else if r.URL.Path != "/v2/alerts/"+alias+"/close" || r.URL.Query().Get("identifierType") != "alias" {
			t.Error("close did not use the original alias")
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"requestId":"` + requestID + `"}`))
	}))
	defer server.Close()
	for _, color := range []string{"yellow", "green"} {
		dispatch := NewDispatch(&CodeOpsgenieJob{URL: server.URL + "/v2/alerts", APIKey: "fixture", Color: color, Message: "safe"}, ecs.Entity{}, "code", color, 0, 0)
		dispatch.MonitorID, dispatch.CatalogUID, dispatch.NotificationCorrelation, dispatch.EndpointID = "service", "incarnation", "warning-episode", "endpoint"
		if result := dispatch.Execute(); result.Err != nil {
			t.Fatal(result.Err)
		}
	}
	if len(paths) != 4 || !strings.HasPrefix(paths[1], "GET /v2/alerts/requests/") || !strings.HasPrefix(paths[3], "GET /v2/alerts/requests/") {
		t.Fatal("acceptance was treated as completed processing", paths)
	}
}

func TestOpsgenieStatusFailureHoldsAcceptedAction(t *testing.T) {
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		posts++
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"requestId":"11111111-1111-4111-8111-111111111111"}`))
	}))
	defer server.Close()
	result := (&CodeOpsgenieJob{URL: server.URL, APIKey: "fixture", Color: "red"}).Execute()
	var rejected *DeliveryError
	if result.Err == nil || errors.As(result.Err, &rejected) || posts != 1 {
		t.Fatal("status-read failure reclassified the accepted effect", result.Err, posts)
	}
}
