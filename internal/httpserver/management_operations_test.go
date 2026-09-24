package httpserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func operationListCredential(t *testing.T, f *managementFixture, id string) *cpra.Response[api.Credential] {
	t.Helper()
	value, err := f.sdk.Credentials.Create(context.Background(), api.Credential{APIVersion: api.APIVersion, Kind: "Credential", Metadata: api.Metadata{ID: id}, Spec: api.CredentialSpec{Value: api.Pointer("operation-list-private-value")}})
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func operationListRejectedCreate(t *testing.T, f *managementFixture, id string) string {
	t.Helper()
	ctx := context.Background()
	value := managementResource("Credential", id, api.CredentialSpec{Value: api.Pointer("never-return-private-list-value")})
	first, err := f.catalog.Prepare(ctx, value, "", true)
	if err != nil {
		t.Fatal(err)
	}
	stale, err := f.catalog.Prepare(ctx, value, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.catalog.CommitAs(ctx, first, "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.catalog.CommitAs(ctx, stale, "operator"); !errors.Is(err, persistence.ErrCatalogConflict) {
		t.Fatal("expected terminal rejected operation", err)
	}
	return stale.OperationID()
}

func TestManagementOperationListFreezesLiveAndTerminalReceipts(t *testing.T) {
	f := newManagementFixture(t, true)
	ctx := context.Background()
	first := operationListCredential(t, f, "service-a")
	_, monitor, _ := configureControlMonitor(t, f, "service-a")
	configureControlMonitor(t, f, "service-b")
	snooze, err := f.sdk.Monitors.Snooze(ctx, monitor.ID, api.ControlRequest{Revision: monitor.ControlRevision, Duration: "1m", Reason: "Maintenance"})
	if err != nil {
		t.Fatal(err)
	}
	rejected := operationListRejectedCreate(t, f, "rejected")
	baseline, err := f.sdk.Operations.List(ctx, cpra.ListOptions{})
	if err != nil || len(baseline.Data.Items) != 6 || baseline.Data.NextCursor != "" {
		t.Fatalf("baseline operation list: %+v %v", baseline, err)
	}
	want := make(map[string]bool)
	for _, item := range baseline.Data.Items {
		want[item.ID] = true
	}
	page, err := f.sdk.Operations.List(ctx, cpra.ListOptions{Limit: 1})
	if err != nil || len(page.Data.Items) != 1 || page.Data.NextCursor == "" || page.Data.Items[0].ID != first.OperationID || page.Data.Items[0].State != "committed" {
		t.Fatalf("first frozen live receipt: %+v %v", page, err)
	}
	// Simulate the internal owner completion signal without starting providers.
	// The receipt moves into terminal history after the HTTP snapshot was taken.
	if err := f.catalog.CompleteOperation(ctx, first.OperationID, true); err != nil {
		t.Fatal(err)
	}
	late := operationListCredential(t, f, "after-snapshot")
	seen := map[string]bool{first.OperationID: true}
	for page.Data.NextCursor != "" {
		cursor := page.Data.NextCursor
		page, err = f.sdk.Operations.List(ctx, cpra.ListOptions{Limit: 1, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		repeated, err := f.sdk.Operations.List(ctx, cpra.ListOptions{Limit: 1, Cursor: cursor})
		if err != nil || !reflect.DeepEqual(page.Data, repeated.Data) {
			t.Fatal("retry changed the frozen operation page", err)
		}
		for _, item := range page.Data.Items {
			if !want[item.ID] || seen[item.ID] || item.ID == late.OperationID {
				t.Fatal("snapshot gained a later operation or duplicated a live-to-terminal transition", item.ID)
			}
			seen[item.ID] = true
			if item.ID == rejected && (item.State != "failed" || !admissionCountIs(item.Committed, 0) || !admissionCountIs(item.Applied, 0)) {
				t.Fatal("terminal rejection claimed an active change")
			}
		}
	}
	if len(seen) != len(want) {
		t.Fatal("snapshot lost original operations")
	}
	current, err := f.sdk.Operations.Get(ctx, first.OperationID)
	if err != nil || current.Data.State != "completed" {
		t.Fatal("direct observation did not expose the later completion", err)
	}
	filtered, err := f.sdk.Operations.List(ctx, cpra.ListOptions{MonitorID: monitor.ID})
	if err != nil || len(filtered.Data.Items) != 2 {
		t.Fatalf("exact monitor operations: %+v %v", filtered, err)
	}
	foundSnooze := false
	for _, item := range filtered.Data.Items {
		if item.ID == first.OperationID || len(item.Items) != 1 || item.Items[0].ID != monitor.ID {
			t.Fatal("same-named shared Credential leaked into Monitor filter")
		}
		foundSnooze = foundSnooze || item.ID == snooze.OperationID
	}
	if !foundSnooze {
		t.Fatal("monitor filter omitted its control operation")
	}
	response, raw := f.request(t, "GET", "/api/v2/operations", managementReaderToken, nil, nil)
	if response.StatusCode != 200 || bytes.Contains(raw, []byte("private-value")) || bytes.Contains(raw, []byte("private-list-value")) {
		t.Fatal("operation list unavailable or contained private configuration")
	}
	var wire api.OperationList
	if api.DecodeResponse(raw, &wire) != nil {
		t.Fatal("operation list does not satisfy SDK contract")
	}
}

func TestManagementOperationListBindsCursorAndAdvertisesRegisteredAccess(t *testing.T) {
	f := newManagementFixture(t, true)
	ctx := context.Background()
	operationListCredential(t, f, "first")
	operationListCredential(t, f, "second")
	capabilities, err := f.sdk.Capabilities(ctx)
	if err != nil || !slices.Contains(capabilities.Data.ResourceOperations["Operation"], "ListOperations") || !slices.Contains(capabilities.Data.ResourceOperations["Operation"], "CreateOperation") || !slices.Contains(capabilities.Data.ResourceOperations["Operation"], "ActivateOperation") {
		t.Fatal("operation discovery does not match registered routes", err)
	}
	access, err := f.sdk.Access(ctx)
	if err != nil || !slices.Contains(access.Data.Permissions, "ListOperations") || !slices.Contains(access.Data.Permissions, "CreateOperation") || !slices.Contains(access.Data.Permissions, "ActivateOperation") {
		t.Fatal("access advertises unsupported collection writes", err)
	}
	page, err := f.sdk.Operations.List(ctx, cpra.ListOptions{Limit: 1})
	if err != nil || page.Data.NextCursor == "" {
		t.Fatal("cursor not returned", err)
	}
	path := "/api/v2/operations?cursor=" + url.QueryEscape(page.Data.NextCursor) + "&limit=1"
	for _, test := range []struct {
		path, token string
		headers     map[string]string
		status      int
	}{
		{"/api/v2/operations", "", nil, 401},
		{"/api/v2/operations", managementReaderToken, map[string]string{"Origin": "https://foreign.invalid"}, 403},
		{path, managementReaderToken, nil, 410},
		{path + "&monitorID=changed", managementOperatorToken, nil, 400},
		{"/api/v2/operations?cursor=" + url.QueryEscape(page.Data.NextCursor) + "&limit=2", managementOperatorToken, nil, 400},
		{"/api/v2/actions?cursor=" + url.QueryEscape(page.Data.NextCursor) + "&limit=1", managementOperatorToken, nil, 410},
		{"/api/v2/operations?selector=team%3Dops", managementReaderToken, nil, 501},
		{"/api/v2/operations?limit=501", managementReaderToken, nil, 400},
		{"/api/v2/operations?limit=1&limit=1", managementReaderToken, nil, 400},
		{"/api/v2/operations?unexpected=1", managementReaderToken, nil, 400},
	} {
		response, _ := f.request(t, "GET", test.path, test.token, nil, test.headers)
		if response.StatusCode != test.status {
			t.Fatalf("%s: got %d, want %d", test.path, response.StatusCode, test.status)
		}
	}
	cleartext := httptest.NewServer(f.http.Config.Handler)
	defer cleartext.Close()
	req, _ := http.NewRequest("GET", cleartext.URL+"/api/v2/operations", nil)
	req.Header.Set("Authorization", "Bearer "+managementReaderToken)
	response, err := cleartext.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("cleartext named identity permitted")
	}
	if err := f.server.cfg.ManagementAuth.Replace(f.authConfig); err != nil {
		t.Fatal(err)
	}
	response, _ = f.request(t, "GET", path, managementOperatorToken, nil, nil)
	if response.StatusCode != 410 {
		t.Fatal("old authority generation retained cursor access")
	}
	page, err = f.sdk.Operations.List(ctx, cpra.ListOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	f.server.managementHTTP.mu.Lock()
	f.server.managementHTTP.now = func() time.Time { return time.Now().Add(6 * time.Minute) }
	f.server.managementHTTP.mu.Unlock()
	response, _ = f.request(t, "GET", "/api/v2/operations?cursor="+url.QueryEscape(page.Data.NextCursor), managementOperatorToken, nil, nil)
	if response.StatusCode != 410 {
		t.Fatal("expired operation cursor retained")
	}
	if err := f.server.StopAdmission(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sdk.Operations.List(ctx, cpra.ListOptions{}); err != nil {
		t.Fatal("draining disabled operation diagnostics", err)
	}
}

func TestManagementOperationListAppliesSharedSnapshotByteBudget(t *testing.T) {
	f := newManagementFixture(t, true)
	operationListCredential(t, f, "first")
	operationListCredential(t, f, "second")
	ctx := context.Background()
	page, err := f.sdk.Operations.List(ctx, cpra.ListOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	m := f.server.managementHTTP
	m.mu.Lock()
	var generation uint64
	var cost int64
	for _, entry := range m.snapshots {
		generation, cost = entry.generation, entry.operationBytes
	}
	if cost <= 0 || m.operationSnapshotBudget("operator") != managementPrincipalOperationBytes-cost {
		m.mu.Unlock()
		t.Fatal("real retained view did not consume its byte budget")
	}
	// Fill the accounting boundary without allocating tens of MiB in a unit
	// fixture. The next HTTP call still uses the real durable snapshot budget.
	m.snapshots["principal-quota-fixture"] = managementSnapshot{principal: "operator", generation: generation, expires: time.Now().Add(time.Minute), operationBytes: managementPrincipalOperationBytes - cost}
	m.mu.Unlock()
	response, _ := f.request(t, "GET", "/api/v2/operations?limit=1", managementOperatorToken, nil, nil)
	if response.StatusCode != 429 {
		t.Fatal("principal aggregate byte quota bypassed")
	}
	// A retained cursor remains usable without allocating a replacement view.
	if _, err := f.sdk.Operations.List(ctx, cpra.ListOptions{Limit: 1, Cursor: page.Data.NextCursor}); err != nil {
		t.Fatal("quota blocked existing cursor", err)
	}
	m.mu.Lock()
	delete(m.snapshots, "principal-quota-fixture")
	remaining := int64(managementOperationSnapshotBytes) - cost
	globalEntries := []string{}
	for remaining > 0 {
		id := fmt.Sprintf("global-quota-fixture-%d", len(globalEntries))
		charge := min(remaining, int64(managementPrincipalOperationBytes))
		m.snapshots[id] = managementSnapshot{principal: id, generation: generation, expires: time.Now().Add(time.Minute), operationBytes: charge}
		globalEntries = append(globalEntries, id)
		remaining -= charge
	}
	m.mu.Unlock()
	response, _ = f.request(t, "GET", "/api/v2/operations?limit=1", managementOperatorToken, nil, nil)
	if response.StatusCode != 429 {
		t.Fatal("global aggregate byte quota bypassed")
	}
	m.mu.Lock()
	for _, id := range globalEntries {
		entry := m.snapshots[id]
		entry.expires = time.Now().Add(-time.Second)
		m.snapshots[id] = entry
	}
	m.mu.Unlock()
	if _, err := f.sdk.Operations.List(ctx, cpra.ListOptions{Limit: 1}); err != nil {
		t.Fatal("expired snapshot did not release its byte budget", err)
	}
}
