package collection

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

func TestResumeDoesNotInferUploadReceiptsFromOptionalCount(t *testing.T) {
	for _, count := range []*int64{api.Pointer(int64(0)), api.Pointer(int64(1))} {
		frozen := freezeTest(t, monitor("a"))
		operations := &fakeOperations{valid: true, operation: api.Operation{IdentityFormat: commitment.Format, ItemCount: api.Pointer(int64(frozen.Len())), ID: "original", State: "staging", ContentDigest: frozen.Digest(), Uploaded: count}}
		result, err := Resume(context.Background(), operations, "original", frozen)
		if err != nil || result.OperationID != "original" || len(operations.uploads) != 1 || len(operations.uploads[0]) != 1 {
			t.Fatal("explicit resume did not replay original bounded items", err)
		}
		if !reflect.DeepEqual(operations.calls, []string{"get", "upload", "validate", "waitValidation", "activate"}) || result.Operation.Uploaded == nil || *result.Operation.Uploaded != int64(frozen.Len()) {
			t.Fatal("resume fabricated progress or created a replacement operation")
		}
	}
}

func TestResumeMissingUploadProgressMakesNoMutation(t *testing.T) {
	for _, state := range []string{"staging", "uploading", "pending", "validated", "applying", "completed", "failed"} {
		frozen := freezeTest(t, monitor("a"))
		operations := &fakeOperations{valid: true, operation: api.Operation{IdentityFormat: commitment.Format, ItemCount: api.Pointer(int64(frozen.Len())), ID: "original", State: state, ContentDigest: frozen.Digest()}}
		result, err := Resume(context.Background(), operations, "original", frozen)
		mutable := state == "staging" || state == "uploading" || state == "pending" || state == "validated"
		if errors.Is(err, ErrProgressUnavailable) != mutable || !reflect.DeepEqual(operations.calls, []string{"get"}) || result.OperationID != "original" || result.Operation.Uploaded != nil {
			t.Fatalf("missing progress caused an inferred receipt or mutation in %s: %v", state, err)
		}
	}
}

func TestResumeRejectsImpossibleReportedUploadCountBeforeMutation(t *testing.T) {
	for _, count := range []int64{-1, 2} {
		frozen := freezeTest(t, monitor("a"))
		operations := &fakeOperations{valid: true, operation: api.Operation{IdentityFormat: commitment.Format, ItemCount: api.Pointer(int64(frozen.Len())), ID: "original", State: "staging", ContentDigest: frozen.Digest(), Uploaded: api.Pointer(count)}}
		result, err := Resume(context.Background(), operations, "original", frozen)
		if err == nil || result.OperationID != "original" || !reflect.DeepEqual(operations.calls, []string{"get"}) {
			t.Fatal("invalid progress caused a mutation or lost the original handle")
		}
	}
}

func TestHTTPResumeMissingUploadProgressMakesNoMutation(t *testing.T) {
	for _, state := range []string{"staging", "uploading", "pending", "validated", "applying", "completed"} {
		t.Run(state, func(t *testing.T) {
			frozen := freezeTest(t, monitor("a"))
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/api/v2/operations/original" {
					t.Error("missing upload progress triggered a mutation")
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"identityFormat": commitment.Format, "itemCount": frozen.Len(), "id": "original", "state": state, "contentDigest": frozen.Digest()})
			}))
			defer server.Close()
			client, err := cpra.New(cpra.Config{BaseURL: server.URL, HTTPClient: server.Client(), AuthToken: "local-test-token"})
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseIdleConnections()
			result, err := Resume(context.Background(), client.Operations, "original", frozen)
			mutable := state != "applying" && state != "completed"
			if errors.Is(err, ErrProgressUnavailable) != mutable || calls.Load() != 1 || result.OperationID != "original" || result.Operation.Uploaded != nil {
				t.Fatal("wire response absence was replaced with a synthetic upload receipt", err)
			}
		})
	}
}
