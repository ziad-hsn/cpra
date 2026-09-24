package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/fleetview"
	"github.com/ziad-hsn/cpra/internal/httpauth"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

const managementOperatorToken = "operator-fixture-012345678901234567890123456"
const managementReaderToken = "reader-fixture-012345678901234567890123456789"
const managementLegacyToken = "legacy-fixture-012345678901234567890123456789"

type managementFixture struct {
	server     *Server
	http       *httptest.Server
	catalog    *management.Catalog
	authConfig httpauth.Config
	sdk        *cpra.Client
}

func newManagementFixture(t *testing.T, verified bool) *managementFixture {
	t.Helper()
	cfg := runtimeconfig.Default()
	cfg.Storage.Mode = "memory"
	store, err := persistence.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	wrapper, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{19}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secureconfig.NewSealer(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := management.NewCatalog(store, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if verified {
		if err := catalog.Verify(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	operator, _ := httpauth.HashToken(managementOperatorToken)
	reader, _ := httpauth.HashToken(managementReaderToken)
	legacy, _ := httpauth.HashToken(managementLegacyToken)
	policy := httpauth.Config{LegacyTokenSHA256: legacy, Principals: []httpauth.Principal{{ID: "operator", Role: httpauth.Operator, TokenSHA256: operator}, {ID: "reader", Role: httpauth.Reader, TokenSHA256: reader}}}
	auth, err := httpauth.New(policy)
	if err != nil {
		t.Fatal(err)
	}
	server := New(ServerConfig{Store: store, Management: catalog, ManagementAuth: auth, AuthToken: managementLegacyToken, Ready: func() bool { return true }}, fleetview.NewHolder(), nil, nil, nil, nil, nil, nil, nil, PublicConfig{})
	mux := http.NewServeMux()
	server.registerAPI(mux)
	server.registerMetrics(mux)
	server.registerSPA(mux)
	transport := httptest.NewTLSServer(server.corsMiddleware(server.authMiddleware(mux)))
	t.Cleanup(transport.Close)
	client, err := cpra.New(cpra.Config{BaseURL: transport.URL, AuthToken: managementOperatorToken, HTTPClient: transport.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return &managementFixture{server, transport, catalog, policy, client}
}
func (f *managementFixture) request(t *testing.T, method, path, token string, body []byte, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, f.http.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if len(body) != 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if method == http.MethodPatch {
		req.Header.Set("Content-Type", "application/merge-patch+json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	response, err := f.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return response, raw
}
func managementResource(kind, id string, spec any) api.Resource {
	raw, _ := json.Marshal(spec)
	return api.Resource{APIVersion: api.APIVersion, Kind: kind, Metadata: api.Metadata{ID: id}, Spec: raw}
}
func (f *managementFixture) create(t *testing.T, r api.Resource) api.Resource {
	t.Helper()
	path := ""
	for _, route := range managementResourceRoutes {
		if route.kind == r.Kind {
			path = route.path
		}
	}
	raw, _ := json.Marshal(r)
	response, body := f.request(t, http.MethodPost, path, managementOperatorToken, raw, map[string]string{"If-None-Match": "*"})
	if response.StatusCode != 200 {
		t.Fatalf("create %s: %d %s", r.Kind, response.StatusCode, body)
	}
	var result api.Resource
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if result.Metadata.UID == "" || result.Metadata.ResourceVersion == "" || response.Header.Get("ETag") != `"`+result.Metadata.ResourceVersion+`"` {
		t.Fatal("missing committed identity", string(body))
	}
	if response.Header.Get("X-CPRa-Admission") != "committed" || response.Header.Get("X-Operation-ID") == "" {
		t.Fatal("misleading admission receipt")
	}
	return result
}
func TestManagementRealSDKAndSharedResources(t *testing.T) {
	f := newManagementFixture(t, true)
	secret := "secret-never-returned-from-catalog"
	credential := f.create(t, managementResource("Credential", "key", api.CredentialSpec{Value: &secret}))
	encoded, _ := json.Marshal(credential)
	if bytes.Contains(encoded, []byte(secret)) || bytes.Contains(encoded, []byte(`"value"`)) {
		t.Fatal("credential leaked")
	}
	f.create(t, managementResource("NotificationEndpoint", "console", api.DriverConfig{Type: "log", Config: json.RawMessage(`{"file":"/tmp/cpra-not-opened-by-validation.log"}`)}))
	f.create(t, managementResource("Recipient", "oncall", api.RecipientSpec{EndpointRefs: []string{"console"}}))
	f.create(t, managementResource("NotificationGroup", "ops", api.NotificationGroupSpec{RecipientRefs: []string{"oncall"}}))
	monitor := api.Monitor{APIVersion: api.APIVersion, Kind: "Monitor", Metadata: api.Metadata{ID: "service"}, Spec: api.MonitorSpec{Check: api.CheckSpec{Interval: "60s", Timeout: "5s", Driver: api.DriverConfig{Type: "http", Config: json.RawMessage(`{"url":"https://example.test/health"}`)}}, Enabled: api.Pointer(false), Notifications: api.Pointer(map[string]api.AlertRule{"red": {NotifyType: api.Pointer("log"), GroupRef: api.Pointer("ops")}})}}
	created, err := f.sdk.Monitors.Create(context.Background(), monitor)
	if err != nil {
		t.Fatal(err)
	}
	if created.ResourceVersion == "" || created.ResourceVersion[0] == '"' {
		t.Fatal("SDK must expose raw opaque resource version")
	}
	got, err := f.sdk.Monitors.Get(context.Background(), "service")
	if err != nil {
		t.Fatal(err)
	}
	replaced, err := f.sdk.Monitors.Replace(context.Background(), "service", got.ResourceVersion, got.Data)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Data.Metadata.UID != got.Data.Metadata.UID {
		t.Fatal("replacement changed incarnation")
	}
	response, raw := f.request(t, http.MethodPatch, "/api/v2/recipients/oncall", managementOperatorToken, []byte(`{"metadata":{"name":"Primary responder"}}`), map[string]string{"If-Match": `"1"`})
	if response.StatusCode != 412 {
		t.Fatalf("stale CAS not explicit: %d %s", response.StatusCode, raw)
	}
	access, err := f.sdk.Access(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(access.Data.Permissions, "CreateRecipient") || !slices.Contains(access.Data.Permissions, "DeleteMonitor") {
		t.Fatal("advertises unavailable action", access.Data)
	}
}
func TestManagementStrictInputsAndReferencedDelete(t *testing.T) {
	f := newManagementFixture(t, true)
	endpoint := f.create(t, managementResource("NotificationEndpoint", "console", api.DriverConfig{Type: "log", Config: json.RawMessage(`{"file":"/tmp/cpra-not-opened-by-validation.log"}`)}))
	f.create(t, managementResource("Recipient", "dependent", api.RecipientSpec{EndpointRefs: []string{"console"}}))
	for _, tc := range []struct {
		name, method, path, body, tag, none, media string
		status                                     int
	}{
		{name: "missing create precondition", method: "POST", path: "/api/v2/recipients", body: `{}`, status: 428},
		{name: "missing update precondition", method: "PATCH", path: "/api/v2/notification-endpoints/console", body: `{}`, status: 428},
		{name: "weak", method: "PATCH", path: "/api/v2/notification-endpoints/console", body: `{}`, tag: `W/"1"`, status: 400},
		{name: "bare", method: "PATCH", path: "/api/v2/notification-endpoints/console", body: `{}`, tag: `1`, status: 400},
		{name: "list", method: "PATCH", path: "/api/v2/notification-endpoints/console", body: `{}`, tag: `"1", "2"`, status: 400},
		{name: "wildcard", method: "PATCH", path: "/api/v2/notification-endpoints/console", body: `{}`, tag: `*`, status: 400},
		{name: "unknown field", method: "POST", path: "/api/v2/recipients", body: `{"apiVersion":"cpra.io/v2","kind":"Recipient","metadata":{"id":"new"},"spec":{"endpointRefs":["console"]},"secret":"must-not-echo"}`, none: "*", status: 400},
		{name: "trailing", method: "POST", path: "/api/v2/recipients", body: `{} {}`, none: "*", status: 400},
		{name: "duplicate", method: "POST", path: "/api/v2/recipients", body: `{"kind":"Recipient","kind":"Monitor"}`, none: "*", status: 400},
		{name: "oversize", method: "POST", path: "/api/v2/recipients", body: strings.Repeat("a", api.MaxResourceBytes+1), none: "*", status: 413},
		{name: "content type", method: "POST", path: "/api/v2/recipients", body: `{}`, none: "*", media: "text/plain", status: 403},
		{name: "referenced deletion rejected", method: "DELETE", path: "/api/v2/notification-endpoints/console", tag: `"` + endpoint.Metadata.ResourceVersion + `"`, status: 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := map[string]string{}
			if tc.tag != "" {
				headers["If-Match"] = tc.tag
			}
			if tc.none != "" {
				headers["If-None-Match"] = tc.none
			}
			if tc.media != "" {
				headers["Content-Type"] = tc.media
			}
			response, raw := f.request(t, tc.method, tc.path, managementOperatorToken, []byte(tc.body), headers)
			if response.StatusCode != tc.status || response.Header.Get("Content-Type") != "application/problem+json" || bytes.Contains(raw, []byte("must-not-echo")) {
				t.Fatalf("got %d %s", response.StatusCode, raw)
			}
		})
	}
	if _, err := f.catalog.Get(context.Background(), "NotificationEndpoint", "console"); err != nil {
		t.Fatal("referenced deletion mutated state", err)
	}
	response, raw := f.request(t, "GET", "/api/v2/discovery", managementOperatorToken, nil, nil)
	if response.StatusCode != 200 || !bytes.Contains(raw, []byte("DeleteNotificationEndpoint")) {
		t.Fatal(string(raw))
	}
}
func TestManagementAuthenticationAndUnverifiedReadiness(t *testing.T) {
	f := newManagementFixture(t, false)
	for _, path := range []string{"/api/v1/healthz", "/api/v1/queues", "/api/v1/pools", "/api/v1/config", "/metrics", "/api/v2/self"} {
		response, raw := f.request(t, "GET", path, managementReaderToken, nil, nil)
		if response.StatusCode != 200 {
			t.Fatalf("named read %s: %d %s", path, response.StatusCode, raw)
		}
	}
	response, _ := f.request(t, "GET", "/api/v1/readyz", managementReaderToken, nil, nil)
	if response.StatusCode != 503 {
		t.Fatal("unverified catalog ready")
	}
	response, _ = f.request(t, "POST", "/api/v2/recipients", managementOperatorToken, []byte(`{}`), map[string]string{"If-None-Match": "*"})
	if response.StatusCode != 400 && response.StatusCode != 503 {
		t.Fatal("unexpected strict decode outcome", response.StatusCode)
	}
	raw, _ := json.Marshal(managementResource("NotificationEndpoint", "blocked", api.DriverConfig{Type: "log", Config: json.RawMessage(`{"file":"/tmp/cpra-not-opened-by-validation.log"}`)}))
	response, _ = f.request(t, "POST", "/api/v2/notification-endpoints", managementOperatorToken, raw, map[string]string{"If-None-Match": "*"})
	if response.StatusCode != 503 {
		t.Fatal("unverified catalog admitted write", response.StatusCode)
	}
	response, _ = f.request(t, "POST", "/api/v2/notification-endpoints", managementReaderToken, raw, map[string]string{"If-None-Match": "*"})
	if response.StatusCode != 403 {
		t.Fatal("reader write", response.StatusCode)
	}
	response, _ = f.request(t, "GET", "/", "", nil, nil)
	if response.StatusCode != 200 {
		t.Fatal("sign-in shell inaccessible")
	}
	req, _ := http.NewRequest("GET", f.http.URL+"/api/v1/healthz", nil)
	req.SetBasicAuth("cpra", managementLegacyToken)
	resp, err := f.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("legacy Basic read broken")
	}
	req, _ = http.NewRequest("POST", f.http.URL+"/api/v2/notification-endpoints", bytes.NewReader(raw))
	req.SetBasicAuth("cpra", managementLegacyToken)
	req.Header.Set("If-None-Match", "*")
	req.Header.Set("Content-Type", "application/json")
	resp, err = f.http.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode < 400 {
		t.Fatal("Basic authorized mutation")
	}
	withoutLegacy := New(ServerConfig{Management: f.catalog, ManagementAuth: f.server.cfg.ManagementAuth}, fleetview.NewHolder(), nil, nil, nil, nil, nil, nil, nil, PublicConfig{})
	mux := http.NewServeMux()
	withoutLegacy.registerAPI(mux)
	request := httptest.NewRequest("GET", "https://cpra.example/api/v1/healthz", nil)
	request.Header.Set("Authorization", "Bearer ")
	recorder := httptest.NewRecorder()
	withoutLegacy.authMiddleware(mux).ServeHTTP(recorder, request)
	if recorder.Code != 401 {
		t.Fatal("management without legacy token admitted empty bearer", recorder.Code)
	}
}
func TestManagementFrozenPaginationAndAdmissionBounds(t *testing.T) {
	f := newManagementFixture(t, true)
	for _, id := range []string{"b", "c", "d"} {
		f.create(t, managementResource("NotificationEndpoint", id, api.DriverConfig{Type: "log", Config: json.RawMessage(`{"file":"/tmp/cpra-not-opened-by-validation.log"}`)}))
	}
	response, raw := f.request(t, "GET", "/api/v2/notification-endpoints?limit=1", managementOperatorToken, nil, nil)
	if response.StatusCode != 200 {
		t.Fatal(string(raw))
	}
	var first managementList
	if err := json.Unmarshal(raw, &first); err != nil {
		t.Fatal(err)
	}
	if first.NextCursor == "" || first.Items[0].Metadata.ID != "b" {
		t.Fatal(string(raw))
	}
	f.create(t, managementResource("NotificationEndpoint", "a", api.DriverConfig{Type: "log", Config: json.RawMessage(`{"file":"/tmp/cpra-not-opened-by-validation.log"}`)}))
	pagePath := "/api/v2/notification-endpoints?cursor=" + url.QueryEscape(first.NextCursor)
	response, second := f.request(t, "GET", pagePath, managementOperatorToken, nil, nil)
	if response.StatusCode != 200 {
		t.Fatal(string(second))
	}
	_, again := f.request(t, "GET", pagePath, managementOperatorToken, nil, nil)
	if !bytes.Equal(second, again) {
		t.Fatal("cursor GET consumed or changed page")
	}
	var next managementList
	_ = json.Unmarshal(second, &next)
	if next.Items[0].Metadata.ID != "c" {
		t.Fatal("insertion changed frozen page")
	}
	for _, tc := range []struct {
		path, token string
		status      int
	}{{pagePath, managementReaderToken, 410}, {pagePath + "&limit=2", managementOperatorToken, 400}, {"/api/v2/notification-endpoints?cursor=not-valid", managementOperatorToken, 400}, {"/api/v2/notification-endpoints?limit=501", managementOperatorToken, 400}, {"/api/v2/notification-endpoints?limit=1&limit=2", managementOperatorToken, 400}, {"/api/v2/notification-endpoints?selector=team=ops", managementOperatorToken, 501}} {
		response, raw := f.request(t, "GET", tc.path, tc.token, nil, nil)
		if response.StatusCode != tc.status {
			t.Fatalf("%s: %d %s", tc.path, response.StatusCode, raw)
		}
	}
	for i := 1; i < managementPrincipalSnapshots; i++ {
		response, _ := f.request(t, "GET", "/api/v2/notification-endpoints?limit=1", managementOperatorToken, nil, nil)
		if response.StatusCode != 200 {
			t.Fatalf("quota early at %d", i)
		}
	}
	response, _ = f.request(t, "GET", "/api/v2/notification-endpoints?limit=1", managementOperatorToken, nil, nil)
	if response.StatusCode != 429 {
		t.Fatal("snapshot quota unbounded")
	}
	if err := f.server.cfg.ManagementAuth.Replace(f.authConfig); err != nil {
		t.Fatal(err)
	}
	response, _ = f.request(t, "GET", pagePath, managementOperatorToken, nil, nil)
	if response.StatusCode != 410 {
		t.Fatal("old policy cursor retained")
	}
	response, raw = f.request(t, "GET", "/api/v2/notification-endpoints?limit=1", managementOperatorToken, nil, nil)
	if response.StatusCode != 200 {
		t.Fatal(string(raw))
	}
	_ = json.Unmarshal(raw, &first)
	f.server.managementHTTP.mu.Lock()
	f.server.managementHTTP.now = func() time.Time { return time.Now().Add(6 * time.Minute) }
	f.server.managementHTTP.mu.Unlock()
	response, _ = f.request(t, "GET", "/api/v2/notification-endpoints?cursor="+url.QueryEscape(first.NextCursor), managementOperatorToken, nil, nil)
	if response.StatusCode != 410 {
		t.Fatal("expired cursor retained")
	}
	for i := 0; i < managementMaxMutations; i++ {
		f.server.managementHTTP.mutations <- struct{}{}
	}
	response, _ = f.request(t, "POST", "/api/v2/recipients", managementOperatorToken, []byte(`{}`), map[string]string{"If-None-Match": "*"})
	if response.StatusCode != 429 || response.Header.Get("Retry-After") == "" {
		t.Fatal("mutation admission not bounded")
	}
	for i := 0; i < managementMaxMutations; i++ {
		<-f.server.managementHTTP.mutations
	}
}
func TestManagementPageByteLimit(t *testing.T) {
	f := newManagementFixture(t, true)
	for i := 0; i < 12; i++ {
		raw, _ := json.Marshal(map[string]string{"url": "https://example.test/" + strings.Repeat("x", 750<<10)})
		resource := managementResource("Monitor", fmt.Sprintf("m%02d", i), api.MonitorSpec{Check: api.CheckSpec{Interval: "60s", Timeout: "5s", Driver: api.DriverConfig{Type: "http", Config: raw}}})
		// This test measures read response bounds. Construct the unusually large
		// fixture through the production catalog so race instrumentation does not
		// turn setup into an unrelated HTTP mutation-deadline assertion. Normal
		// HTTP write admission remains covered by the mutation contract tests.
		prepared, err := f.catalog.Prepare(context.Background(), resource, "", true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.catalog.CommitAs(context.Background(), prepared, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	response, raw := f.request(t, "GET", "/api/v2/monitors?limit=500", managementOperatorToken, nil, nil)
	if response.StatusCode != 200 {
		t.Fatal(string(raw))
	}
	if len(raw) > managementMaxPageBytes {
		t.Fatal("response byte bound exceeded", len(raw))
	}
	var page managementList
	_ = json.Unmarshal(raw, &page)
	if len(page.Items) == 0 || len(page.Items) >= 12 || page.NextCursor == "" {
		t.Fatal("byte bound did not produce continuation")
	}
}

func TestManagementRoutesRequireExplicitPairedConfiguration(t *testing.T) {
	disabled := New(ServerConfig{}, fleetview.NewHolder(), nil, nil, nil, nil, nil, nil, nil, PublicConfig{})
	mux := http.NewServeMux()
	disabled.registerAPI(mux)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest("GET", "/api/v2/self", nil))
	if recorder.Code != 404 {
		t.Fatal("unconfigured management route exists")
	}
	f := newManagementFixture(t, true)
	for _, config := range []ServerConfig{{Addr: "127.0.0.1:0", Management: f.catalog}, {Addr: "127.0.0.1:0", ManagementAuth: f.server.cfg.ManagementAuth}} {
		invalid := New(config, fleetview.NewHolder(), nil, nil, nil, nil, nil, nil, nil, PublicConfig{})
		if err := invalid.Start(); err == nil {
			invalid.Stop()
			t.Fatal("unpaired management configuration accepted")
		}
	}
}

func TestManagementPrepareDeleteHasNoEffectUntilCommit(t *testing.T) {
	f := newManagementFixture(t, true)
	resource := f.create(t, managementResource("NotificationEndpoint", "temporary", api.DriverConfig{Type: "log", Config: json.RawMessage(`{"file":"/tmp/cpra-not-opened-by-validation.log"}`)}))
	prepared, err := f.server.managementHTTP.prepareChange(context.Background(), http.MethodDelete, resource.Kind, resource.Metadata.ID, resource.Metadata.ResourceVersion, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.catalog.Get(context.Background(), resource.Kind, resource.Metadata.ID); err != nil {
		t.Fatal("preparation deleted active resource")
	}
	if _, err := f.catalog.Commit(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	if _, err := f.catalog.Get(context.Background(), resource.Kind, resource.Metadata.ID); err == nil {
		t.Fatal("internal committed deletion was lost")
	}
}

func TestManagementMutationReceiptsRemainCommittedUntilOwnerApplies(t *testing.T) {
	f := newManagementFixture(t, true)
	endpoint := api.NotificationEndpoint{APIVersion: api.APIVersion, Kind: "NotificationEndpoint", Metadata: api.Metadata{ID: "temporary"}, Spec: api.DriverConfig{Type: "log", Config: json.RawMessage(`{"file":"/tmp/cpra-not-opened-by-validation.log"}`)}}
	created, err := f.sdk.NotificationEndpoints.Create(context.Background(), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if created.OperationID == "" {
		t.Fatal("creation has no reconcile identity")
	}
	operation, err := f.sdk.Operations.Get(context.Background(), created.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if operation.Data.State != "committed" || (operation.Data.Applied == nil || *operation.Data.Applied != 0) || (operation.Data.Committed == nil || *operation.Data.Committed != 1) {
		t.Fatal("HTTP pretended owner applied", operation.Data)
	}
	deleted, err := f.sdk.NotificationEndpoints.Delete(context.Background(), endpoint.Metadata.ID, created.ResourceVersion)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.OperationID == "" || deleted.Data.ID != deleted.OperationID || deleted.Data.State != "committed" || (deleted.Data.Applied == nil || *deleted.Data.Applied != 0) {
		t.Fatal("delete returned invented or applied receipt", deleted)
	}
	response, _ := f.request(t, "GET", "/api/v2/notification-endpoints/temporary", managementReaderToken, nil, nil)
	if response.StatusCode != 404 {
		t.Fatal("committed desired deletion absent")
	}
	superseded, err := f.sdk.Operations.Get(context.Background(), created.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if superseded.Data.State != "partial" || (superseded.Data.Applied == nil || *superseded.Data.Applied != 0) {
		t.Fatal("old operation lost supersession", superseded.Data)
	}
	// This explicitly simulates the separate owner; no HTTP handler is allowed to
	// claim this step merely because the catalog write committed.
	if err := f.catalog.CompleteOperation(context.Background(), deleted.OperationID, true); err != nil {
		t.Fatal(err)
	}
	completed, err := f.sdk.Operations.Get(context.Background(), deleted.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Data.State != "completed" || (completed.Data.Applied == nil || *completed.Data.Applied != 1) {
		t.Fatal("durable owner completion not observable", completed.Data)
	}
	response, _ = f.request(t, "GET", "/api/v2/operations/not-an-operation", managementReaderToken, nil, nil)
	if response.StatusCode != 404 {
		t.Fatal("unknown operation not explicit", response.StatusCode)
	}
}
