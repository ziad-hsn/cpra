//go:build externaljobs

package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ziad-hsn/cpra/internal/fleetview"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func httpJobType(id string) api.JobType {
	return api.JobType{APIVersion: api.APIVersion, Kind: "JobType", Metadata: api.Metadata{ID: id}, Spec: api.JobTypeSpec{Kind: "check", Version: "v1", Handler: "check", ProtocolVersion: "1", Timeout: "5s", ParameterSchema: json.RawMessage(`{"type":"object","properties":{"large":{"const":9007199254740993}},"additionalProperties":false}`), ResultSchema: json.RawMessage(`{"const":"private-schema-canary"}`)}}
}

func enableJobTypeFixture(t *testing.T, f *managementFixture, enabled bool) {
	t.Helper()
	f.http.Close()
	config := f.server.cfg
	config.ExternalJobsEnabled = enabled
	f.server = New(config, fleetview.NewHolder(), nil, nil, nil, nil, nil, nil, nil, PublicConfig{})
	mux := http.NewServeMux()
	f.server.registerAPI(mux)
	f.http = httptest.NewTLSServer(f.server.corsMiddleware(f.server.authMiddleware(mux)))
	t.Cleanup(f.http.Close)
	var err error
	f.sdk, err = cpra.New(cpra.Config{BaseURL: f.http.URL, AuthToken: managementOperatorToken, HTTPClient: f.http.Client()})
	if err != nil {
		t.Fatal(err)
	}
}

func newJobTypeHTTPFixture(t *testing.T) (*managementFixture, *persistence.Store, string) {
	t.Helper()
	f, store, dir, _ := newPreflightRaftFixture(t)
	validationHTTPAuthority(t, f)
	enableJobTypeFixture(t, f, true)
	return f, store, dir
}

func TestJobTypeHTTPRealSDKReceiptsAndRestart(t *testing.T) {
	f, store, dir := newJobTypeHTTPFixture(t)
	empty, err := f.sdk.JobTypes().List(t.Context(), cpra.ListOptions{})
	if err != nil || empty.Data.Items == nil || len(empty.Data.Items) != 0 {
		t.Fatal("empty list", empty, err)
	}
	created, err := f.sdk.JobTypes().Create(t.Context(), httpJobType("custom-check"))
	if err != nil {
		t.Fatal(err)
	}
	if created.OperationID == "" || created.ResourceVersion != created.Data.Metadata.ResourceVersion {
		t.Fatal("missing commit identity", created)
	}
	receipt, err := f.sdk.Operations.Get(t.Context(), created.OperationID)
	if err != nil || receipt.Data.State != "completed" || receipt.Data.Committed == nil || *receipt.Data.Committed != 1 || receipt.Data.Applied == nil || *receipt.Data.Applied != 1 {
		t.Fatal("registration receipt", receipt, err)
	}
	first, ok, err := store.JobType(t.Context(), "custom-check")
	if err != nil || !ok {
		t.Fatal(err)
	}
	fetched, err := f.sdk.JobTypes().Get(t.Context(), "custom-check")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(fetched.Data.Spec.ParameterSchema, []byte("9007199254740993")) {
		t.Fatal("schema number rounded")
	}
	changed := fetched.Data
	changed.Metadata.Name = api.Pointer("New name")
	replaced, err := f.sdk.JobTypes().Replace(t.Context(), "custom-check", fetched.ResourceVersion, changed)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := store.JobType(t.Context(), "custom-check")
	if err != nil || !reflect.DeepEqual(first.Versions, second.Versions) {
		t.Fatal("metadata change replaced immutable ciphertext", err)
	}
	if _, err = f.sdk.JobTypes().Replace(t.Context(), "custom-check", created.ResourceVersion, changed); !errors.Is(err, cpra.ErrConflict) {
		var problem *cpra.Error
		if !errors.As(err, &problem) || problem.StatusCode != 412 {
			t.Fatal("stale CAS", err)
		}
	}
	changed = replaced.Data
	changed.Spec.Timeout = "10s"
	_, err = f.sdk.JobTypes().Replace(t.Context(), "custom-check", replaced.ResourceVersion, changed)
	var problem *cpra.Error
	if !errors.As(err, &problem) || problem.StatusCode != 409 {
		t.Fatal("immutable version", err)
	}
	removed, err := f.sdk.JobTypes().Delete(t.Context(), "custom-check", replaced.ResourceVersion)
	if err != nil || removed.Data.ID != removed.OperationID || removed.Data.State != "completed" {
		t.Fatal("delete receipt", removed, err)
	}
	recreated := httpJobType("custom-check")
	recreated.Spec.Version = "v2"
	current, err := f.sdk.JobTypes().Create(t.Context(), recreated)
	if err != nil || current.Data.Metadata.UID == created.Data.Metadata.UID {
		t.Fatal("recreation", current, err)
	}
	history, err := store.History().Page("resource/JobType/custom-check", "", 500)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(history)
	if bytes.Contains(encoded, []byte("private-schema-canary")) || bytes.Contains(encoded, []byte("9007199254740993")) {
		t.Fatal("schema leaked into history")
	}
	if !bytes.Contains(encoded, []byte(created.OperationID)) || !bytes.Contains(encoded, []byte(`"operator"`)) {
		t.Fatal("missing operation audit", string(encoded))
	}
	f.http.Close()
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := runtimeconfig.Default()
	cfg.Storage.Directory = dir
	reopened, err := persistence.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	wrapper, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{19}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secureconfig.NewSealer(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := management.NewCatalog(reopened, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if err = catalog.Verify(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.catalog = catalog
	f.server.cfg.Store = reopened
	f.server.cfg.Management = catalog
	enableJobTypeFixture(t, f, true)
	got, err := f.sdk.JobTypes().Get(t.Context(), "custom-check")
	if err != nil || !reflect.DeepEqual(got.Data, current.Data) {
		t.Fatal("restart resource", got, err)
	}
	for _, id := range []string{created.OperationID, replaced.OperationID, removed.OperationID, current.OperationID} {
		got, err := f.sdk.Operations.Get(t.Context(), id)
		if err != nil || got.Data.State != "completed" {
			t.Fatal("restart receipt", id, got, err)
		}
	}
	retained, ok, err := reopened.JobTypeVersion(t.Context(), "custom-check", "v1")
	if err != nil || !ok || !reflect.DeepEqual(first.Versions["v1"], retained) {
		t.Fatal("retained original lost", err)
	}
}

func TestJobTypeHTTPGatesAndDiscovery(t *testing.T) {
	f, _, _ := newJobTypeHTTPFixture(t)
	for _, enabled := range []bool{true, false} {
		enableJobTypeFixture(t, f, enabled)
		response, body := f.request(t, "GET", "/api/v2/job-types", managementOperatorToken, nil, nil)
		want := 404
		if enabled {
			want = 200
		}
		if response.StatusCode != want {
			t.Fatal("runtime gate", enabled, response.StatusCode, string(body))
		}
		access, err := f.sdk.Access(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for _, op := range jobTypeOperations {
			if slices.Contains(access.Data.Permissions, op.operation) != enabled {
				t.Fatal("advertised access", op.operation, enabled)
			}
		}
		response, body = f.request(t, "GET", "/api/v2/discovery", managementOperatorToken, nil, nil)
		var discovery api.Capabilities
		if response.StatusCode != 200 || json.Unmarshal(body, &discovery) != nil {
			t.Fatal("discovery", string(body))
		}
		if slices.Contains(discovery.Resources, "JobType") != enabled || len(discovery.ResourceOperations["JobType"]) != map[bool]int{true: 5, false: 0}[enabled] {
			t.Fatal("discovery gate", discovery)
		}
		for _, path := range []string{"/api/v2/external-workers", "/api/v2/external-workers/poll", "/api/v2/external-workers/start"} {
			method := "POST"
			if path == "/api/v2/external-workers" {
				method = "GET"
			}
			response, _ := f.request(t, method, path, managementOperatorToken, []byte(`{}`), nil)
			if response.StatusCode != 404 {
				t.Fatal("unimplemented worker route", path, response.StatusCode)
			}
		}
	}
}

func TestJobTypeHTTPInvalidInputHasNoMutation(t *testing.T) {
	f, store, _ := newJobTypeHTTPFixture(t)
	good, _ := json.Marshal(httpJobType("custom-check"))
	tests := []struct {
		name, method, path, token string
		body                      []byte
		headers                   map[string]string
		want                      int
	}{
		{"missing auth", "POST", "/api/v2/job-types", "", good, map[string]string{"If-None-Match": "*"}, 401},
		{"reader", "POST", "/api/v2/job-types", managementReaderToken, good, map[string]string{"If-None-Match": "*"}, 403},
		{"legacy", "POST", "/api/v2/job-types", managementLegacyToken, good, map[string]string{"If-None-Match": "*"}, 401},
		{"missing precondition", "POST", "/api/v2/job-types", managementOperatorToken, good, nil, 428},
		{"wrong precondition", "POST", "/api/v2/job-types", managementOperatorToken, good, map[string]string{"If-Match": "*"}, 428},
		{"duplicate field", "POST", "/api/v2/job-types", managementOperatorToken, bytes.Replace(good, []byte(`"kind":"JobType"`), []byte(`"kind":"JobType","kind":"JobType"`), 1), map[string]string{"If-None-Match": "*"}, 400},
		{"unknown field", "POST", "/api/v2/job-types", managementOperatorToken, append([]byte(`{"unexpected":true,`), good[1:]...), map[string]string{"If-None-Match": "*"}, 400},
		{"trailing body", "POST", "/api/v2/job-types", managementOperatorToken, append(append([]byte{}, good...), []byte(`{}`)...), map[string]string{"If-None-Match": "*"}, 400},
		{"path mismatch", "PUT", "/api/v2/job-types/another", managementOperatorToken, good, map[string]string{"If-Match": `"version"`}, 400},
		{"encoded body", "POST", "/api/v2/job-types", managementOperatorToken, good, map[string]string{"If-None-Match": "*", "Content-Encoding": "gzip"}, 415},
		{"cross origin", "POST", "/api/v2/job-types", managementOperatorToken, good, map[string]string{"If-None-Match": "*", "Origin": "https://foreign.invalid"}, 403},
		{"oversize", "POST", "/api/v2/job-types", managementOperatorToken, []byte(strings.Repeat("x", api.MaxResourceBytes+1)), map[string]string{"If-None-Match": "*"}, 413},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := store.Status().CommittedIndex
			response, raw := f.request(t, tc.method, tc.path, tc.token, tc.body, tc.headers)
			if response.StatusCode != tc.want {
				t.Fatal("status", response.StatusCode, string(raw))
			}
			if store.Status().CommittedIndex != before || response.Header.Get("X-Operation-ID") != "" {
				t.Fatal("invalid input mutated state")
			}
		})
	}
	for _, schema := range []string{`{"$ref":"https://invalid/schema"}`, `{"script":"private-canary"}`, `{"type":"nonsense"}`} {
		resource := httpJobType("bad-schema")
		resource.Spec.ParameterSchema = json.RawMessage(schema)
		raw, _ := json.Marshal(resource)
		before := store.Status().CommittedIndex
		response, body := f.request(t, "POST", "/api/v2/job-types", managementOperatorToken, raw, map[string]string{"If-None-Match": "*"})
		if response.StatusCode != 422 || store.Status().CommittedIndex != before || bytes.Contains(body, []byte("private-canary")) {
			t.Fatal("invalid schema", response.StatusCode, string(body))
		}
	}
}

type jobTypeLostResponseTransport struct {
	base  http.RoundTripper
	calls atomic.Int64
	lost  atomic.Bool
}

func (t *jobTypeLostResponseTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(r)
	if r.Method == "POST" && r.URL.Path == "/api/v2/job-types" {
		t.calls.Add(1)
		if err == nil && response.StatusCode == 200 && t.lost.CompareAndSwap(false, true) {
			_ = response.Body.Close()
			response.Body = io.NopCloser(&jobTypeLostBody{})
		}
	}
	return response, err
}

type jobTypeLostBody struct{}

func (*jobTypeLostBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func TestJobTypeHTTPLostResponseKeepsOriginalReceipt(t *testing.T) {
	f, _, _ := newJobTypeHTTPFixture(t)
	transport := &jobTypeLostResponseTransport{base: f.http.Client().Transport}
	client, err := cpra.New(cpra.Config{BaseURL: f.http.URL, AuthToken: managementOperatorToken, HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.JobTypes().Create(context.Background(), httpJobType("lost-response"))
	if !errors.Is(err, cpra.ErrAmbiguous) || result == nil || result.OperationID == "" || transport.calls.Load() != 1 {
		t.Fatal("missing uncertain identity", result, err)
	}
	operation, err := client.Operations.Get(t.Context(), result.OperationID)
	if err != nil || operation.Data.State != "completed" {
		t.Fatal("original receipt", operation, err)
	}
	got, err := client.JobTypes().Get(t.Context(), "lost-response")
	if err != nil || got.Data.Metadata.ResourceVersion != operation.Data.Items[0].NewVersion {
		t.Fatal("original mutation", got, err)
	}
	if transport.calls.Load() != 1 {
		t.Fatal("automatic mutation retry")
	}
}

func TestJobTypeHTTPAuthorizesBeforeBodyRead(t *testing.T) {
	f, _, _ := newJobTypeHTTPFixture(t)
	for _, token := range []string{"", managementReaderToken, managementLegacyToken} {
		body := &jobTypeCountingBody{}
		request := httptest.NewRequest("POST", "https://cpra.test/api/v2/job-types", body)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("If-None-Match", "*")
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		recorder := httptest.NewRecorder()
		f.server.managementHTTP.handleJobType(recorder, request, "CreateJobType")
		if recorder.Code != 401 && recorder.Code != 403 {
			t.Fatal("authorization status", recorder.Code)
		}
		if body.reads.Load() != 0 {
			t.Fatal("unauthorized body read")
		}
	}
}

type jobTypeCountingBody struct{ reads atomic.Int64 }

func (r *jobTypeCountingBody) Read([]byte) (int, error) { r.reads.Add(1); return 0, io.EOF }
func (*jobTypeCountingBody) Close() error               { return nil }

func TestJobTypeHTTPStableEncryptedCursor(t *testing.T) {
	f, _, _ := newJobTypeHTTPFixture(t)
	original := map[string]api.JobType{}
	for _, id := range []string{"item-1", "item-2", "item-3", "item-4"} {
		result, err := f.sdk.JobTypes().Create(t.Context(), httpJobType(id))
		if err != nil {
			t.Fatal(err)
		}
		original[id] = result.Data
	}
	page, err := f.sdk.JobTypes().List(t.Context(), cpra.ListOptions{Limit: 2})
	if err != nil || len(page.Data.Items) != 2 || page.Data.NextCursor == "" {
		t.Fatal("first page", page, err)
	}
	changed := original["item-4"]
	changed.Metadata.Name = api.Pointer("after capture")
	if _, err = f.sdk.JobTypes().Replace(t.Context(), "item-4", changed.Metadata.ResourceVersion, changed); err != nil {
		t.Fatal(err)
	}
	if _, err = f.sdk.JobTypes().Delete(t.Context(), "item-3", original["item-3"].Metadata.ResourceVersion); err != nil {
		t.Fatal(err)
	}
	recreated := httpJobType("item-3")
	recreated.Spec.Version = "v2"
	for _, r := range []api.JobType{recreated, httpJobType("item-0")} {
		if _, err = f.sdk.JobTypes().Create(t.Context(), r); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		next, err := f.sdk.JobTypes().List(t.Context(), cpra.ListOptions{Limit: 2, Cursor: page.Data.NextCursor})
		if err != nil || next.Data.NextCursor != "" || !reflect.DeepEqual(next.Data.Items, []api.JobType{original["item-3"], original["item-4"]}) {
			t.Fatal("cursor mixed generations", next, err)
		}
	}
	response, _ := f.request(t, "GET", "/api/v2/job-types?limit=2&cursor="+url.QueryEscape(page.Data.NextCursor), managementReaderToken, nil, nil)
	if response.StatusCode != 410 {
		t.Fatal("cross principal cursor", response.StatusCode)
	}
	for query, want := range map[string]int{"limit=0": 400, "limit=501": 400, "limit=1&limit=2": 400, "unexpected=1": 400, "selector=owner%3Dteam": 501, "monitorID=monitor": 501} {
		response, _ := f.request(t, "GET", "/api/v2/job-types?"+query, managementOperatorToken, nil, nil)
		if response.StatusCode != want {
			t.Fatal("query", query, response.StatusCode)
		}
	}
	now, err := f.sdk.JobTypes().List(t.Context(), cpra.ListOptions{})
	if err != nil || len(now.Data.Items) != 5 || now.Data.Items[0].Metadata.ID != "item-0" || now.Data.Items[3].Metadata.UID == original["item-3"].Metadata.UID {
		t.Fatal("new generation", now, err)
	}
}

func TestJobTypeHTTPLargePageIsBounded(t *testing.T) {
	f := newManagementFixture(t, true)
	validationHTTPAuthority(t, f)
	enableJobTypeFixture(t, f, true)
	schema := json.RawMessage(`{"const":"` + strings.Repeat("x", 60<<10) + `"}`)
	for i := range 70 {
		r := httpJobType(fmt.Sprintf("large-%03d", i))
		r.Spec.ParameterSchema = schema
		r.Spec.ResultSchema = schema
		if _, err := f.sdk.JobTypes().Create(t.Context(), r); err != nil {
			t.Fatal("create", i, err)
		}
	}
	cursor := ""
	seen := map[string]bool{}
	pages := 0
	for {
		response, raw := f.request(t, "GET", "/api/v2/job-types?limit=500&cursor="+url.QueryEscape(cursor), managementOperatorToken, nil, nil)
		if response.StatusCode != 200 || len(raw) > managementMaxPageBytes {
			t.Fatal("page size/status", len(raw), response.StatusCode)
		}
		var page api.JobTypeList
		if err := api.StrictDecode(raw, &page); err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			if seen[item.Metadata.ID] {
				t.Fatal("duplicate", item.Metadata.ID)
			}
			seen[item.Metadata.ID] = true
		}
		pages++
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
		if pages > 3 {
			t.Fatal("cursor made no progress")
		}
	}
	if len(seen) != 70 || pages != 2 {
		t.Fatal("lost bounded rows", len(seen), pages)
	}
}

func TestJobTypeServerRequiresRaftManagement(t *testing.T) {
	f := newManagementFixture(t, true)
	f.server.cfg.ExternalJobsEnabled = true
	if err := f.server.Start(); err == nil {
		f.server.Stop()
		t.Fatal("external management started with volatile storage")
	}
}
