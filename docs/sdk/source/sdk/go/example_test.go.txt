package cpra_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"

	"github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func ExampleNew() {
	client, err := cpra.New(cpra.Config{BaseURL: "https://cpra.example.net", AuthToken: "token-from-user-configuration"})
	if err != nil {
		panic(err)
	}
	defer client.CloseIdleConnections()
	fmt.Println(client != nil)
	// Output: true
}

// This local fixture demonstrates the request and returned version metadata.
// It implements one candidate response, not a CPRa management server.
func ExampleMonitorsService_Get() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/monitors/payments-http" ||
			r.Header.Get("Authorization") != "Bearer fixture-token" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ETag", "fixture-version-1")
		w.Header().Set("X-Request-ID", "fixture-request-1")
		io.WriteString(w, `{"apiVersion":"cpra.io/v2","kind":"Monitor","metadata":{"id":"payments-http"},"spec":{"check":{"driver":{"type":"http","config":{"url":"https://payments.example.test/health"}},"interval":"60s","timeout":"5s"}},"status":{}}`)
	}))
	defer server.Close()
	client, err := cpra.New(cpra.Config{
		BaseURL: server.URL, AuthToken: "fixture-token",
		AllowInsecureHTTP: true, // Only this example's loopback fixture uses HTTP.
	})
	if err != nil {
		panic(err)
	}
	defer client.CloseIdleConnections()
	monitor, err := client.Monitors.Get(context.Background(), "payments-http")
	if err != nil {
		panic(err)
	}
	fmt.Println(monitor.Data.Metadata.ID)
	fmt.Println(monitor.ResourceVersion, monitor.RequestID)
	// Output:
	// payments-http
	// fixture-version-1 fixture-request-1
}

func Example_driver() {
	driver, err := api.Driver("check", "http", api.PulseHTTPConfig{URL: api.Pointer("https://service.example.net/health")})
	if err != nil {
		panic(err)
	}
	resource := api.Monitor{APIVersion: api.APIVersion, Kind: "Monitor", Metadata: api.Metadata{ID: "payments"}, Spec: api.MonitorSpec{Check: api.CheckSpec{Driver: driver, Interval: "60s", Timeout: "5s"}, Enabled: api.Pointer(false)}}
	encoded, err := json.Marshal(resource)
	if err != nil {
		panic(err)
	}
	fmt.Println(api.ValidateResourceValue(resource) == nil, len(encoded) > 0)
	// Output: true true
}

// The following examples illustrate the draft v2 contract. They compile but have
// no Output directive, so go test does not execute their network requests. They
// are usage examples, not evidence that the current server implements v2.
func ExampleMonitorsService_Create_lifecycle() {
	client, err := cpra.New(cpra.Config{
		BaseURL:   "https://cpra.example.net",
		AuthToken: "token-from-user-configuration",
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	defer client.CloseIdleConnections()
	ctx := context.Background()
	driver, err := api.Driver("check", "http", api.PulseHTTPConfig{
		URL: api.Pointer("https://service.example.net/health"),
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	created, err := client.Monitors.Create(ctx, api.Monitor{
		APIVersion: api.APIVersion,
		Kind:       "Monitor",
		Metadata:   api.Metadata{ID: "payments-http"},
		Spec: api.MonitorSpec{Check: api.CheckSpec{
			Driver: driver, Interval: "60s", Timeout: "5s",
		}},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	current, err := client.Monitors.Get(ctx, created.Data.Metadata.ID)
	if err != nil {
		fmt.Println(err)
		return
	}
	current.Data.Metadata.Name = api.Pointer("Payments health endpoint")
	updated, err := client.Monitors.Replace(ctx, current.Data.Metadata.ID,
		current.ResourceVersion, current.Data)
	if err != nil {
		// A version conflict requires an explicit decision by the caller.
		fmt.Println(err)
		return
	}
	removed, err := client.Monitors.Delete(ctx, updated.Data.Metadata.ID,
		updated.ResourceVersion)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("Deletion operation:", removed.Data.ID)
}

func ExampleMonitorsService_Patch() {
	client, err := cpra.New(cpra.Config{BaseURL: "https://cpra.example.net", AuthToken: "token-from-user-configuration"})
	if err != nil {
		fmt.Println(err)
		return
	}
	defer client.CloseIdleConnections()
	ctx := context.Background()
	current, err := client.Monitors.Get(ctx, "payments-http")
	if err != nil {
		fmt.Println(err)
		return
	}
	// Use the exact observed version. Null removes the optional recovery spec;
	// false remains distinct from an omitted enabled field.
	updated, err := client.Monitors.Patch(ctx, "payments-http", current.ResourceVersion,
		api.MergePatch(`{"spec":{"enabled":false,"recovery":null}}`))
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("New version:", updated.ResourceVersion)
}

func ExampleIncidentsService_Acknowledge() {
	client, err := cpra.New(cpra.Config{BaseURL: "https://cpra.example.net", AuthToken: "token-from-user-configuration"})
	if err != nil {
		fmt.Println(err)
		return
	}
	defer client.CloseIdleConnections()
	ctx := context.Background()
	incident, err := client.Incidents.Get(ctx, "incident-from-monitor-status")
	if err != nil {
		fmt.Println(err)
		return
	}
	acknowledged, err := client.Incidents.Acknowledge(ctx, incident.Data.ID,
		api.ControlRequest{Revision: incident.Data.Revision, Note: "Investigating the database connection"})
	if err != nil {
		fmt.Println(err)
		return
	}
	// The authenticated principal supplies the recorded actor. Acknowledging
	// does not pause checks, recovery, or notifications.
	fmt.Println("Acknowledged by:", acknowledged.Data.AcknowledgedBy)
}

func ExampleIncidentsService_Dismiss() {
	client, err := cpra.New(cpra.Config{BaseURL: "https://cpra.example.net", AuthToken: "token-from-user-configuration"})
	if err != nil {
		fmt.Println(err)
		return
	}
	defer client.CloseIdleConnections()
	ctx := context.Background()
	incident, err := client.Incidents.Get(ctx, "incident-from-monitor-status")
	if err != nil {
		fmt.Println(err)
		return
	}
	dismissed, err := client.Incidents.Dismiss(ctx, incident.Data.ID,
		api.ControlRequest{Revision: incident.Data.Revision, Reason: "Known incident tracked by the service team"})
	if err != nil {
		fmt.Println(err)
		return
	}
	// Dismiss pauses this incident's notifications; checks and recovery continue.
	fmt.Println("Notifications dismissed:", dismissed.Data.Dismissed)
}

func ExampleMonitorsService_Snooze() {
	client, err := cpra.New(cpra.Config{BaseURL: "https://cpra.example.net", AuthToken: "token-from-user-configuration"})
	if err != nil {
		fmt.Println(err)
		return
	}
	defer client.CloseIdleConnections()
	ctx := context.Background()
	monitor, err := client.Monitors.Get(ctx, "payments-http")
	if err != nil {
		fmt.Println(err)
		return
	}
	paused, err := client.Monitors.Snooze(ctx, "payments-http", api.ControlRequest{
		Revision: monitor.Data.Status.ControlRevision,
		Duration: "30m", Reason: "Scheduled maintenance",
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("Snooze operation:", paused.Data.ID)
}

func ExampleMonitorsService_Disable() {
	client, err := cpra.New(cpra.Config{BaseURL: "https://cpra.example.net", AuthToken: "token-from-user-configuration"})
	if err != nil {
		fmt.Println(err)
		return
	}
	defer client.CloseIdleConnections()
	ctx := context.Background()
	monitor, err := client.Monitors.Get(ctx, "payments-http")
	if err != nil {
		fmt.Println(err)
		return
	}
	disabled, err := client.Monitors.Disable(ctx, "payments-http", monitor.ResourceVersion)
	if err != nil {
		fmt.Println(err)
		return
	}
	// Disable persists until an explicit Enable, preserving other controls.
	fmt.Println("Disabled monitor:", disabled.Data.Metadata.ID)
}

func ExampleMonitorsService_Iterate() {
	client, err := cpra.New(cpra.Config{BaseURL: "https://cpra.example.net", AuthToken: "token-from-user-configuration"})
	if err != nil {
		fmt.Println(err)
		return
	}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	monitors := client.Monitors.Iterate(cpra.ListOptions{Limit: 100, Selector: "service=payments"})
	for monitors.Next(ctx) {
		monitor := monitors.Value()
		fmt.Println(monitor.Metadata.ID, monitor.Status.Health)
	}
	if err := monitors.Err(); err != nil {
		fmt.Println(err)
	}
}

func ExampleClient_Metrics() {
	client, err := cpra.New(cpra.Config{BaseURL: "https://cpra.example.net", AuthToken: "token-from-user-configuration"})
	if err != nil {
		fmt.Println(err)
		return
	}
	defer client.CloseIdleConnections()
	metrics, err := client.Metrics(context.Background())
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, queue := range metrics.Data.Queues {
		fmt.Println(queue.Name, queue.Depth, queue.Capacity, queue.Saturated)
	}
}

func ExampleClient_Prometheus() {
	client, err := cpra.New(cpra.Config{BaseURL: "https://cpra.example.net", AuthToken: "token-from-user-configuration"})
	if err != nil {
		fmt.Println(err)
		return
	}
	defer client.CloseIdleConnections()
	if err := client.Prometheus(context.Background(), os.Stdout); err != nil {
		fmt.Println(err)
	}
}
