package cpra_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func operationWaitTLSFixture(t *testing.T, state string) (*cpra.Client, api.Operation, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	operation := api.Operation{ID: "original-operation", State: state, ContentDigest: "original-content",
		Uploaded: api.Pointer(int64(3)), Committed: api.Pointer(int64(0)), Validated: api.Pointer(false), RetryAfterSeconds: 60}
	if state == "validated" {
		operation.Validated = api.Pointer(true)
	}
	requests, mutations := new(atomic.Int32), new(atomic.Int32)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet {
			mutations.Add(1)
		}
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/operations/original-operation" || r.URL.RawQuery != "" || r.TLS == nil || r.Header.Get("Authorization") != "Bearer wait-fixture-token" {
			t.Error("wait changed its read-only authenticated TLS request")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-ID", "original-request")
		w.Header().Set("X-Operation-ID", operation.ID)
		w.Header().Set("ETag", `"original-version"`)
		w.Header().Set("Retry-After", "30")
		if err := json.NewEncoder(w).Encode(operation); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	client, err := cpra.New(cpra.Config{BaseURL: server.URL, AuthToken: "wait-fixture-token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return client, operation, requests, mutations
}

func assertOriginalWaitResponse(t *testing.T, response *cpra.Response[api.Operation], original api.Operation) {
	t.Helper()
	if response == nil || !reflect.DeepEqual(response.Data, original) || response.RequestID != "original-request" ||
		response.OperationID != original.ID || response.ResourceVersion != "original-version" ||
		response.StatusCode != http.StatusOK || response.RetryAfter != 30*time.Second {
		t.Fatal("wait did not preserve the original operation response", response)
	}
}

func TestOperationsWaitCanceledSpellingsTLS(t *testing.T) {
	for _, state := range []string{"canceled", "cancelled", "rejected", "invalidated"} {
		t.Run(state, func(t *testing.T) {
			client, original, requests, mutations := operationWaitTLSFixture(t, state)
			// A terminal response returns despite long retry intervals in both
			// header and body. A regression is bounded by this caller deadline.
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			response, err := client.Operations.Wait(ctx, original.ID)
			if err != nil {
				t.Fatal("terminal cancellation waited instead of returning", err)
			}
			assertOriginalWaitResponse(t, response, original)
			if requests.Load() != 1 || mutations.Load() != 0 {
				t.Fatal("terminal cancellation polled or mutated", requests.Load(), mutations.Load())
			}
		})
	}
}

func TestOperationsWaitValidatedRemainsPendingTLS(t *testing.T) {
	client, original, requests, mutations := operationWaitTLSFixture(t, "validated")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	response, err := client.Operations.Wait(ctx, original.ID)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("validation was treated as completed operation execution", err)
	}
	assertOriginalWaitResponse(t, response, original)
	if requests.Load() != 1 || mutations.Load() != 0 {
		t.Fatal("canceling a pending wait polled or mutated", requests.Load(), mutations.Load())
	}
}
