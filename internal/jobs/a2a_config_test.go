package jobs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/manifest"
	"gopkg.in/yaml.v3"
)

func TestA2AConfigurationBuildsWithoutProviderIO(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	for _, encoding := range []string{"json", "yaml"} {
		t.Run(encoding, func(t *testing.T) {
			decode := json.Unmarshal
			if encoding == "yaml" {
				decode = yaml.Unmarshal // JSON is also a supported YAML document.
			}
			var recovery manifest.Intervention
			if err := decode([]byte(`{"action":"a2a","target":{"url":"`+server.URL+`","token":"test-token","tenant":"ops","request_timeout":"2s","task_timeout":"3s","allow_insecure_http":true}}`), &recovery); err != nil {
				t.Fatal(err)
			}
			job, err := CreateInterventionJob(recovery, ecs.Entity{})
			if err != nil {
				t.Fatal(err)
			}
			agent, ok := job.(*A2AJob)
			if !ok || agent.taskTimeout != 3*time.Second || agent.pollInterval > agent.taskTimeout || agent.client.http.Timeout != 2*time.Second {
				t.Fatal("configuration did not retain bounded execution settings")
			}
			var endpoint manifest.Endpoint
			if err := decode([]byte(`{"type":"a2a","config":{"url":"`+server.URL+`","request_timeout":"2s","timeout":"3s","allow_insecure_http":true}}`), &endpoint); err != nil {
				t.Fatal(err)
			}
			job, err = CreateCodeJob("monitor", manifest.CodeConfig{Notify: endpoint.Type, Config: endpoint.Config}, ecs.Entity{}, "red")
			if err != nil {
				t.Fatal(err)
			}
			if job.Execute().Err == nil {
				t.Fatal("configuration supplied execution authority")
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatal("loading or constructing a driver contacted the agent")
	}
	for _, kind := range []string{"recovery", "notification"} {
		if err := ValidateDriver(kind, "a2a"); err != nil {
			t.Fatal(err)
		}
	}
}
