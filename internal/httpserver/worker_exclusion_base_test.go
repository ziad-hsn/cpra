//go:build !externaljobs

package httpserver

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestWorkerDefaultBuildExcludesSchemaAndRoutes(t *testing.T) {
	var schema struct {
		Paths      map[string]json.RawMessage `json:"paths"`
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(api.Schema(), &schema); err != nil {
		t.Fatal(err)
	}
	for path := range schema.Paths {
		if strings.HasPrefix(path, "/api/v2/external-workers") || strings.HasPrefix(path, "/api/v2/job-types") {
			t.Fatal("default schema exposes an extension route", path)
		}
	}
	for name := range schema.Components.Schemas {
		if strings.HasPrefix(name, "Worker") || strings.HasPrefix(name, "JobType") || name == "ExternalConfig" {
			t.Fatal("default schema exposes an extension model", name)
		}
	}
	fixture := newManagementFixture(t, true)
	for _, path := range []string{
		"/api/v2/external-workers/poll", "/api/v2/external-workers/start",
		"/api/v2/external-workers/heartbeat", "/api/v2/external-workers/result",
		"/api/v2/external-workers/late-evidence",
	} {
		response, _ := fixture.request(t, http.MethodPost, path, managementOperatorToken, []byte(`{}`), nil)
		if response.StatusCode != http.StatusNotFound {
			t.Fatal("default build registered a worker protocol route", path, response.StatusCode)
		}
	}
	for _, path := range []string{"/api/v2/external-workers", "/api/v2/job-types"} {
		response, _ := fixture.request(t, http.MethodGet, path, managementOperatorToken, nil, nil)
		if response.StatusCode != http.StatusNotFound {
			t.Fatal("default build registered an extension observation route", path, response.StatusCode)
		}
	}
}
