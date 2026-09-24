package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func collectionHTTPIdentity(p api.PreflightRequest) api.CollectionPrepareRequest {
	return api.CollectionPrepareRequest{IdentityFormat: api.CollectionPrepareRequestIdentityFormat(p.IdentityFormat),
		IdentityKey: p.IdentityKey, SourceFingerprint: p.SourceFingerprint, ContentDigest: p.ContentDigest, ItemCount: p.ItemCount}
}

func TestCollectionAdmissionHTTPStagesOriginalBytesWithoutActivation(t *testing.T) {
	f, store, _, _ := newPreflightRaftFixture(t)
	secret := "unique-collection-secret-not-returned"
	frozen := preflightInventory(t, managementResource("Credential", "service-token", api.CredentialSpec{Value: &secret}), preflightMonitor("app", "https://example.test"))
	request := collectionHTTPIdentity(frozen)
	raw, _ := json.Marshal(request)
	resp, body := f.request(t, http.MethodPost, "/api/v2/collections/prepare", managementOperatorToken, raw, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("prepare: %d %s", resp.StatusCode, body)
	}
	var ticket api.CollectionAdmission
	if json.Unmarshal(body, &ticket) != nil || ticket.Ticket == "" {
		t.Fatal("missing ticket")
	}
	create := api.OperationCreateRequest{AdmissionTicket: ticket.Ticket, IdentityFormat: api.OperationCreateRequestIdentityFormat(request.IdentityFormat),
		IdentityKey: request.IdentityKey, SourceFingerprint: request.SourceFingerprint, ContentDigest: request.ContentDigest, ItemCount: request.ItemCount}
	raw, _ = json.Marshal(create)
	resp, body = f.request(t, http.MethodPost, "/api/v2/operations", managementOperatorToken, raw, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("create: %d %s", resp.StatusCode, body)
	}
	var operation api.Operation
	if json.Unmarshal(body, &operation) != nil || operation.ID == "" || operation.State != "uploading" {
		t.Fatal("invalid create result")
	}
	if strings.Contains(string(body), *frozen.IdentityKey) || strings.Contains(string(body), ticket.Ticket) {
		t.Fatal("private creation fields returned")
	}
	resp, body = f.request(t, http.MethodPost, "/api/v2/operations", managementOperatorToken, raw, nil)
	var retry api.Operation
	if resp.StatusCode != 200 || json.Unmarshal(body, &retry) != nil || retry.ID != operation.ID {
		t.Fatal("lost-create retry identity changed")
	}
	path := "/api/v2/operations/" + operation.ID
	// A stale-body edit without recomputing the original item MAC cannot enter staging.
	upload := api.UploadRequest{Items: []api.ApplyItem{frozen.Items[0]}}
	bad := upload
	bad.Items = append([]api.ApplyItem(nil), upload.Items...)
	bad.Items[0].ContentDigest = strings.Repeat("0", 64)
	raw, _ = json.Marshal(bad)
	resp, _ = f.request(t, http.MethodPut, path+"/items", managementOperatorToken, raw, nil)
	if resp.StatusCode != 422 {
		t.Fatal("changed original MAC accepted", resp.StatusCode)
	}
	for _, item := range frozen.Items {
		raw, _ = json.Marshal(api.UploadRequest{Items: []api.ApplyItem{item}})
		resp, body = f.request(t, http.MethodPut, path+"/items", managementOperatorToken, raw, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("upload: %d %s", resp.StatusCode, body)
		}
		if strings.Contains(string(body), secret) {
			t.Fatal("uploaded secret returned")
		}
	}
	// Exact retry of an earlier ordinal reconciles the existing encrypted row.
	raw, _ = json.Marshal(upload)
	resp, body = f.request(t, http.MethodPut, path+"/items", managementOperatorToken, raw, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("original-row retry: %d %s", resp.StatusCode, body)
	}
	resp, body = f.request(t, http.MethodGet, path, managementReaderToken, nil, nil)
	if resp.StatusCode != 404 || strings.Contains(string(body), operation.ID) {
		t.Fatalf("reader accessed another actor's collection: %d", resp.StatusCode)
	}
	resp, body = f.request(t, http.MethodGet, path, managementOperatorToken, nil, nil)
	if resp.StatusCode != 200 || json.Unmarshal(body, &retry) != nil || retry.Uploaded == nil || *retry.Uploaded != 2 || retry.Validated != nil || retry.Committed == nil || *retry.Committed != 0 {
		t.Fatalf("progress: %d %s", resp.StatusCode, body)
	}
	view, err := store.CatalogSnapshot()
	if err != nil || view.Len() != 0 {
		t.Fatal("upload activated resources", err)
	}
	if err := f.server.StopAdmission(context.Background()); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(create)
	resp, _ = f.request(t, http.MethodPost, "/api/v2/operations", managementOperatorToken, raw, nil)
	if resp.StatusCode != 503 {
		t.Fatal("draining accepted creation", resp.StatusCode)
	}
	resp, _ = f.request(t, http.MethodGet, path, managementOperatorToken, nil, nil)
	if resp.StatusCode != 200 {
		t.Fatal("diagnostics stopped during drain", resp.StatusCode)
	}
}

func TestCollectionAdmissionHTTPAuthorizationAndBounds(t *testing.T) {
	f := newManagementFixture(t, true)
	for _, route := range []struct{ method, path string }{{"POST", "/api/v2/collections/prepare"}, {"POST", "/api/v2/operations"}, {"PUT", "/api/v2/operations/not-found/items"}} {
		for _, token := range []string{"", managementReaderToken} {
			resp, body := f.request(t, route.method, route.path, token, []byte("secret malformed input"), nil)
			if resp.StatusCode != 401 && resp.StatusCode != 403 {
				t.Fatal("body handled before authorization", route.path, resp.StatusCode)
			}
			if strings.Contains(string(body), "secret malformed input") {
				t.Fatal("error echoed input")
			}
		}
		resp, _ := f.request(t, route.method, route.path, managementOperatorToken, []byte(`{}`), map[string]string{"Content-Encoding": "gzip"})
		if resp.StatusCode != 415 {
			t.Fatal("encoded body accepted", route.path, resp.StatusCode)
		}
		resp, _ = f.request(t, route.method, route.path+"?input=secret", managementOperatorToken, []byte(`{}`), nil)
		if resp.StatusCode != 400 {
			t.Fatal("query accepted", route.path, resp.StatusCode)
		}
	}
	request := collectionHTTPIdentity(preflightInventory(t, preflightMonitor("app", "https://example.test")))
	raw, _ := json.Marshal(request)
	resp, body := f.request(t, "POST", "/api/v2/collections/prepare", managementOperatorToken, raw, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("prepare: %d %s", resp.StatusCode, body)
	}
	var ticket api.CollectionAdmission
	if json.Unmarshal(body, &ticket) != nil || !ticket.ExpiresAt.After(time.Now()) {
		t.Fatal("ticket lifetime absent")
	}
	resp, body = f.request(t, "GET", "/api/v2/self", managementOperatorToken, nil, nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "PrepareCollection") || !strings.Contains(string(body), "UploadOperation") || !strings.Contains(string(body), "ActivateOperation") {
		t.Fatal("discovery advertises unsupported activation")
	}
}

func TestCollectionAdmissionHTTPProfilePresence(t *testing.T) {
	f := newManagementFixture(t, true)
	request := collectionHTTPIdentity(preflightInventory(t, preflightMonitor("app", "https://example.test")))
	raw, _ := json.Marshal(request)
	resp, body := f.request(t, "POST", "/api/v2/collections/prepare", managementOperatorToken, raw, nil)
	var ticket api.CollectionAdmission
	if resp.StatusCode != 200 || json.Unmarshal(body, &ticket) != nil {
		t.Fatal("legacy prepare", resp.StatusCode)
	}
	create := api.OperationCreateRequest{AdmissionTicket: ticket.Ticket, IdentityFormat: api.OperationCreateRequestIdentityFormat(request.IdentityFormat),
		IdentityKey: request.IdentityKey, SourceFingerprint: request.SourceFingerprint, ContentDigest: request.ContentDigest, ItemCount: request.ItemCount}
	for _, route := range []struct {
		path    string
		request any
	}{{"/api/v2/collections/prepare", request}, {"/api/v2/operations", create}} {
		base, _ := json.Marshal(route.request)
		for _, profile := range []string{`null`, `""`, `false`, `1`, `{}`, `[]`, `"cpra.file.future.v2"`} {
			input := append(append([]byte(nil), base[:len(base)-1]...), []byte(`,"normalizationProfile":`+profile+`}`)...)
			resp, body := f.request(t, "POST", route.path, managementOperatorToken, input, nil)
			if resp.StatusCode != 422 || strings.Contains(string(body), *request.IdentityKey) || resp.Header.Get("X-Operation-ID") != "" {
				t.Fatal("invalid profile accepted, allocated or echoed private input", route.path, profile, resp.StatusCode)
			}
		}
	}
}
