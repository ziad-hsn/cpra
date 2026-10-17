package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestManagementValidationProblemKeepsSafeCauseOnly(t *testing.T) {
	const marker = "PRIVATE_PROVIDER_PAYLOAD"
	err := errors.Join(management.ErrValidation, errors.New(marker), api.InvalidField("spec.check.driver.config.url", "required"))
	w := httptest.NewRecorder()
	w.Header().Set("X-Request-ID", "request-fixture")
	writeManagementError(w, err)
	var result api.Problem
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || w.Code != 422 || len(result.Errors) != 1 || result.RequestID != "request-fixture" {
		t.Fatal("validation problem contract", w.Code, w.Body.String())
	}
	if result.Errors[0].Field != "spec.check.driver.config.url" || result.Errors[0].Reason != "required" || strings.Contains(w.Body.String(), marker) {
		t.Fatal("safe cause lost or private error included")
	}
}

func TestManagementValidationShapeErrorsThroughMutationHandlers(t *testing.T) {
	f := newManagementFixture(t, true)
	original := f.create(t, managementResource("Monitor", "schema-check", api.MonitorSpec{Check: api.CheckSpec{Interval: "1s", Timeout: "1s", Driver: api.DriverConfig{Type: "http", Config: json.RawMessage(`{"url":"https://example.test"}`)}}}))
	const marker = "PRIVATE_UNRECOGNIZED_VALUE"
	for _, method := range []string{http.MethodPost, http.MethodPut} {
		for _, input := range []struct{ name, spec, reason string }{
			{"unknown", `{"PRIVATE_UNRECOGNIZED_VALUE":"PRIVATE_UNRECOGNIZED_VALUE"}`, "unknownField"},
			{"duplicate", `{"check":{},"check":{}}`, "duplicateField"},
			{"type", `{"check":{"interval":19,"timeout":"1s","driver":{"type":"http","config":{"url":"https://example.test"}}}}`, "invalidType"},
		} {
			t.Run(method+"/"+input.name, func(t *testing.T) {
				body, _ := json.Marshal(managementResource("Monitor", "schema-check", json.RawMessage(input.spec)))
				path := "/api/v2/monitors"
				headers := map[string]string{"If-None-Match": "*"}
				if method == http.MethodPut {
					path += "/schema-check"
					headers = map[string]string{"If-Match": `"` + original.Metadata.ResourceVersion + `"`}
				}
				response, raw := f.request(t, method, path, managementOperatorToken, body, headers)
				var problem api.Problem
				if response.StatusCode != 400 || json.Unmarshal(raw, &problem) != nil || len(problem.Errors) != 1 || problem.Errors[0].Reason != input.reason || problem.RequestID == "" || strings.Contains(string(raw), marker) {
					t.Fatalf("lost safe decoder diagnostic: %d %s", response.StatusCode, raw)
				}
			})
		}
	}
	current, err := f.catalog.Get(t.Context(), "Monitor", "schema-check")
	if err != nil || current.Metadata.ResourceVersion != original.Metadata.ResourceVersion {
		t.Fatal("invalid request changed the monitor", err)
	}
}
