package collection

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

func TestHTTPResumeAfterCommittedUploadLosesReply(t *testing.T) {
	var mu sync.Mutex
	operation := api.Operation{ID: "epoch.1", State: "staging", IdentityFormat: commitment.Format}
	stored := map[string]string{}
	items := map[int64]api.ApplyItem{}
	dropped := false
	activated := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer api-test-token" {
			t.Error("missing API authentication")
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v2/collections/prepare":
			_ = json.NewEncoder(w).Encode(api.CollectionAdmission{Ticket: "original-fixture-ticket", ExpiresAt: time.Now().Add(time.Hour)})
			return
		case "POST /api/v2/operations":
			var request api.OperationCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				return
			}
			if request.AdmissionTicket != "original-fixture-ticket" {
				t.Error("creation did not carry original admission")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			operation.ContentDigest = request.ContentDigest
			operation.ItemCount = api.Pointer(request.ItemCount)
		case "GET /api/v2/operations/epoch.1":
		case "PUT /api/v2/operations/epoch.1/items":
			var request api.UploadRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				return
			}
			for _, item := range request.Items {
				if old, ok := stored[item.ID]; ok && old != item.ContentDigest {
					w.WriteHeader(409)
					return
				}
				stored[item.ID] = item.ContentDigest
				items[item.Ordinal] = item
			}
			operation.Uploaded = api.Pointer(int64(len(stored)))
			if !dropped {
				dropped = true
				connection, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = connection.Close()
				return
			}
		case "POST /api/v2/operations/epoch.1/validate":
			operation.State = "validating"
			w.WriteHeader(202)
			_ = json.NewEncoder(w).Encode(operation)
			return
		case "GET /api/v2/operations/epoch.1/validation":
			page := validationPageFixture(operation, true)
			for i := range page.Items {
				item := items[page.Items[i].Ordinal]
				page.Items[i].Kind = item.Resource.Kind
				page.Items[i].ID = item.Resource.Metadata.ID
				page.Items[i].Source = item.Source
				page.Items[i].SourceDocument = item.SourceDocument
				page.Items[i].SourceItem = item.SourceItem
			}
			_ = json.NewEncoder(w).Encode(page)
			return
		case "POST /api/v2/operations/epoch.1/activate":
			activated++
			operation.State = "applying"
		default:
			t.Error("unexpected route", r.Method, r.URL.Path)
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(operation)
	}))
	defer server.Close()
	client, err := cpra.New(cpra.Config{BaseURL: server.URL, AuthToken: "api-test-token", AllowInsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	f := freezeTest(t, "["+monitor("a")+","+monitor("b")+"]")
	partial, err := Apply(context.Background(), client.Operations, f)
	if !errors.Is(err, cpra.ErrAmbiguous) || partial.OperationID != "epoch.1" {
		t.Fatal(partial, err)
	}
	mu.Lock()
	before := activated
	mu.Unlock()
	if before != 0 {
		t.Fatal("activated after lost upload reply")
	}
	result, err := Resume(context.Background(), client.Operations, partial.OperationID, f)
	if err != nil {
		t.Fatal(err)
	}
	if result.Operation.State != "applying" {
		t.Fatal(result)
	}
	mu.Lock()
	defer mu.Unlock()
	if activated != 1 || len(stored) != 2 {
		t.Fatalf("activation=%d distinct uploads=%d", activated, len(stored))
	}
}

func TestLocalDryRunLimitCausesNoRequest(t *testing.T) {
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { count++ }))
	defer server.Close()
	client, err := cpra.New(cpra.Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	var input strings.Builder
	input.WriteString("[")
	for i := 0; i < 8; i++ {
		if i > 0 {
			input.WriteByte(',')
		}
		resource := monitor(string(rune('a' + i)))
		resource = strings.Replace(resource, `"headers":{"X_Service":"keep_key"}`, `"body":"`+strings.Repeat("x", 600<<10)+`"`, 1)
		input.WriteString(resource)
	}
	input.WriteByte(']')
	f := freezeTest(t, input.String())
	if _, err = Preflight(context.Background(), client.Operations, f); err == nil {
		t.Fatal("oversized ephemeral request accepted")
	}
	if count != 0 {
		t.Fatal("oversized preflight made server request")
	}
}
