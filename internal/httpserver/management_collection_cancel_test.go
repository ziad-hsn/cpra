package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/httpauth"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func collectionCancellationHTTPSetup(t *testing.T, f *managementFixture, upload bool) (api.Operation, api.UploadRequest) {
	t.Helper()
	p := preflightInventory(t, preflightMonitor("first", "https://first.example.test"), preflightMonitor("second", "https://second.example.test"))
	identity := collectionHTTPIdentity(p)
	raw, err := json.Marshal(identity)
	if err != nil {
		t.Fatal(err)
	}
	response, body := f.request(t, http.MethodPost, "/api/v2/collections/prepare", managementOperatorToken, raw, nil)
	var ticket api.CollectionAdmission
	if response.StatusCode != 200 || json.Unmarshal(body, &ticket) != nil || ticket.Ticket == "" {
		t.Fatal("prepare", response.StatusCode)
	}
	create := api.OperationCreateRequest{AdmissionTicket: ticket.Ticket, IdentityFormat: api.OperationCreateRequestIdentityFormat(identity.IdentityFormat), IdentityKey: identity.IdentityKey, SourceFingerprint: identity.SourceFingerprint, ContentDigest: identity.ContentDigest, ItemCount: identity.ItemCount}
	raw, err = json.Marshal(create)
	if err != nil {
		t.Fatal(err)
	}
	response, body = f.request(t, http.MethodPost, "/api/v2/operations", managementOperatorToken, raw, nil)
	var op api.Operation
	if response.StatusCode != 200 || json.Unmarshal(body, &op) != nil || op.ID == "" {
		t.Fatal("create", response.StatusCode)
	}
	if upload {
		raw, err = json.Marshal(api.UploadRequest{Items: p.Items[:1]})
		if err != nil {
			t.Fatal(err)
		}
		response, _ = f.request(t, http.MethodPut, "/api/v2/operations/"+op.ID+"/items", managementOperatorToken, raw, nil)
		if response.StatusCode != 200 {
			t.Fatal("upload first row", response.StatusCode)
		}
	}
	return op, api.UploadRequest{Items: p.Items[1:]}
}

func TestCollectionCancellationHTTPOriginalReceiptAndInactivePrefix(t *testing.T) {
	f := newManagementFixture(t, true)
	op, remaining := collectionCancellationHTTPSetup(t, f, true)
	store := f.server.cfg.Store
	path := "/api/v2/operations/" + op.ID
	response, body := f.request(t, http.MethodPost, path+"/cancel", managementOperatorToken, nil, nil)
	var canceled api.Operation
	if response.StatusCode != 200 || response.TLS == nil || json.Unmarshal(body, &canceled) != nil || canceled.ID != op.ID || canceled.State != "canceled" || canceled.Uploaded == nil || *canceled.Uploaded != 1 {
		t.Fatalf("cancel original prefix over TLS: HTTP %d", response.StatusCode)
	}
	head, exists, err := store.CollectionGet(op.ID)
	if err != nil || !exists || head.Phase != "canceled" || head.Cancellation == nil {
		t.Fatal("original cancellation metadata missing", err)
	}
	raw, err := json.Marshal(remaining)
	if err != nil {
		t.Fatal(err)
	}
	response, _ = f.request(t, http.MethodPut, path+"/items", managementOperatorToken, raw, nil)
	if response.StatusCode != 410 && response.StatusCode != 409 {
		t.Fatal("canceled upload accepted later row", response.StatusCode)
	}
	result, err := store.Submit(context.Background(), []persistence.Command{{Kind: "collection", At: time.Now().UTC(), Collection: &persistence.CollectionCommand{
		Action: "cleanup", OperationID: op.ID, UploadID: head.UploadID, Cleanup: &persistence.CollectionCleanup{
			ActivityAt: head.ActivityAt, Uploaded: head.Uploaded, EncodedBytes: head.EncodedBytes}}}})
	if err != nil || len(result) != 1 || result[0].Err != nil {
		t.Fatal("cleanup", err)
	}
	if _, exists, _ := store.CollectionGet(op.ID); exists {
		t.Fatal("cleanup left original header")
	}
	index := store.Status().CommittedIndex
	// This is the same request a caller makes after losing the first response.
	response, body = f.request(t, http.MethodPost, path+"/cancel", managementOperatorToken, nil, nil)
	var repeated api.Operation
	if response.StatusCode != 200 || json.Unmarshal(body, &repeated) != nil || repeated.ID != canceled.ID || repeated.State != "canceled" || repeated.Uploaded == nil || *repeated.Uploaded != 1 || store.Status().CommittedIndex != index {
		t.Fatal("cleanup retry did not reconcile without another command", response.StatusCode)
	}
	events, err := store.History().Page("collection/"+op.ID, "", 100)
	if err != nil || len(events.Events) != 1 || events.Events[0].ActionID != head.Cancellation.ID {
		t.Fatal("cancellation was duplicated or changed", err)
	}
	view, err := store.CatalogSnapshot()
	if err != nil || view.Len() != 0 {
		t.Fatal("inactive cancellation changed active resources", err)
	}
}

func TestCollectionCancellationHTTPAuthorizationBodyAndExpiry(t *testing.T) {
	f := newManagementFixture(t, true)
	op, _ := collectionCancellationHTTPSetup(t, f, false)
	path := "/api/v2/operations/" + op.ID + "/cancel"
	store := f.server.cfg.Store
	index := store.Status().CommittedIndex
	const anotherToken = "another-operator-fixture-012345678901234567890"
	hash, err := httpauth.HashToken(anotherToken)
	if err != nil {
		t.Fatal(err)
	}
	policy := f.authConfig
	policy.Principals = append(append([]httpauth.Principal(nil), policy.Principals...), httpauth.Principal{ID: "another", Role: httpauth.Operator, TokenSHA256: hash})
	if err := f.server.managementHTTP.auth.Replace(policy); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		token  string
		status int
	}{{"", 401}, {managementReaderToken, 403}, {anotherToken, 404}} {
		response, _ := f.request(t, http.MethodPost, path, row.token, nil, nil)
		if response.StatusCode != row.status {
			t.Fatal("unauthorized cancellation", response.StatusCode, "want", row.status)
		}
	}
	for _, input := range []string{" ", "{}", strings.Repeat("x", 1<<20)} {
		response, _ := f.request(t, http.MethodPost, path, managementOperatorToken, []byte(input), nil)
		if response.StatusCode < 400 || response.StatusCode >= 500 {
			t.Fatal("body was accepted", response.StatusCode)
		}
	}
	response, _ := f.request(t, http.MethodPost, path+"?reason=private", managementOperatorToken, nil, nil)
	if response.StatusCode != 400 {
		t.Fatal("query was accepted", response.StatusCode)
	}
	response, _ = f.request(t, http.MethodPost, path, managementOperatorToken, nil, map[string]string{"Content-Encoding": "gzip"})
	if response.StatusCode != 415 {
		t.Fatal("encoded body was accepted", response.StatusCode)
	}
	old := fmt.Sprintf("/api/v2/operations/op.%s.%020d/cancel", uuid.NewString(), 1)
	response, _ = f.request(t, http.MethodPost, old, managementOperatorToken, nil, nil)
	if response.StatusCode != 410 {
		t.Fatal("old epoch was accepted", response.StatusCode)
	}
	head, _, _ := store.CollectionGet(op.ID)
	// Only this server's existing injected observation clock is advanced.
	f.server.managementHTTP.now = func() time.Time { return head.ExpiresAt }
	response, _ = f.request(t, http.MethodPost, path, managementOperatorToken, nil, nil)
	if response.StatusCode != 410 || store.Status().CommittedIndex != index {
		t.Fatal("expired upload revived or invalid request changed storage", response.StatusCode)
	}
}

func TestCollectionCancellationHTTPConcurrentRetriesAndShutdown(t *testing.T) {
	f := newManagementFixture(t, true)
	op, _ := collectionCancellationHTTPSetup(t, f, false)
	path := "/api/v2/operations/" + op.ID + "/cancel"
	store := f.server.cfg.Store
	index := store.Status().CommittedIndex
	const callers = 4
	var wg sync.WaitGroup
	failures := make(chan error, callers)
	start := make(chan struct{})
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			req, err := http.NewRequest(http.MethodPost, f.http.URL+path, nil)
			if err != nil {
				failures <- err
				return
			}
			req.Header.Set("Authorization", "Bearer "+managementOperatorToken)
			response, err := f.http.Client().Do(req)
			if err != nil {
				failures <- err
				return
			}
			defer response.Body.Close()
			raw, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
			var result api.Operation
			if err != nil || response.StatusCode != 200 || json.Unmarshal(raw, &result) != nil || result.ID != op.ID || result.State != "canceled" {
				failures <- fmt.Errorf("concurrent cancellation failed with HTTP %d", response.StatusCode)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	if store.Status().CommittedIndex != index+1 {
		t.Fatal("concurrent requests submitted duplicate commands")
	}
	if err := f.server.StopAdmission(context.Background()); err != nil {
		t.Fatal(err)
	}
	response, _ := f.request(t, http.MethodPost, path, managementOperatorToken, nil, nil)
	if response.StatusCode != 503 {
		t.Fatal("draining server accepted cancellation", response.StatusCode)
	}
	response, _ = f.request(t, http.MethodGet, "/api/v2/operations/"+op.ID, managementOperatorToken, nil, nil)
	if response.StatusCode != 200 {
		t.Fatal("shutdown disabled original receipt reads", response.StatusCode)
	}
}
