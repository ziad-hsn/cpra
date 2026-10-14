package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

func TestResumeCompleteCollectionVerifiesFilesAndUsesOriginalHandle(t *testing.T) {
	protocol := newUploadRecoveryCLIProtocol(t, "uploading")
	protocol.operation.Uploaded = api.Pointer(int64(2))
	protocol.attempt.OperationUploaded = 2
	base := "/api/v2/operations/" + sequencedOperationID
	fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != base+"/validate" && r.URL.Path != base+"/validation" && r.URL.Path != base+"/activate" {
			protocol.serve(w, r)
			return
		}
		protocol.mu.Lock()
		defer protocol.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case base + "/validate":
			protocol.calls = append(protocol.calls, "validate")
			protocol.operation.State = "validating"
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(protocol.operation)
		case base + "/validation":
			protocol.calls = append(protocol.calls, "validation")
			at := time.Now().UTC()
			page := api.ValidationResultPage{OperationID: sequencedOperationID, IdentityFormat: commitment.Format, ContentDigest: protocol.operation.ContentDigest, ItemCount: 2,
				Summary: api.ValidationResultSummary{ResultID: "validation", Valid: true, Count: 2, Digest: strings.Repeat("a", 64), CapabilitiesDigest: strings.Repeat("b", 64), PlanID: "plan", PlanDigest: strings.Repeat("c", 64), FinalizedAt: at, ExpiresAt: at.Add(time.Hour)}}
			token, _ := commitment.SourceToken(1)
			for n, id := range []string{"one", "two"} {
				page.Items = append(page.Items, api.ValidationResultItem{Ordinal: int64(n + 1), Kind: "Monitor", ID: id, Source: token, SourceDocument: 1, SourceItem: int64(n + 1), Change: "create"})
			}
			_ = json.NewEncoder(w).Encode(page)
		case base + "/activate":
			protocol.calls = append(protocol.calls, "activate")
			protocol.operation.State = "applying"
			protocol.operation.ExecutionResult = &api.ExecutionResultAvailability{State: "pending"}
			_ = json.NewEncoder(w).Encode(protocol.operation)
		}
	})
	stdout, stderr, err := fixture.run(t, []byte("exact original source"), "resume", "operation/"+sequencedOperationID, "-f", "-", "-o", "json")
	var operation api.Operation
	if err != nil || json.Unmarshal([]byte(stdout), &operation) != nil || operation.ID != sequencedOperationID || operation.State != "applying" {
		t.Fatal("continuation lost original operation", stdout, stderr, err)
	}
	want := []string{"get-original", "create-attempt", "upload-source", "verify", "resume", "get-original", "get-original", "validate", "validation", "activate"}
	if !reflect.DeepEqual(protocol.calls, want) || len(protocol.parts) != 1 || string(protocol.parts[0]) != "exact original source" || !strings.Contains(stderr, cliUploadAttemptID) {
		t.Fatal("complete input was not verified or original phases duplicated", protocol.calls, stderr)
	}
}

func TestResumeRequiresOriginalInputsBeforeRequests(t *testing.T) {
	var requests int
	fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(500) })
	_, _, err := fixture.run(t, nil, "resume", "operation/"+sequencedOperationID)
	if err == nil || requests != 0 {
		t.Fatal("continuation without inputs made a request", err, requests)
	}
}

func TestResumeInterruptedValidationReportsOriginalDisposition(t *testing.T) {
	protocol := newUploadRecoveryCLIProtocol(t, "uploading")
	protocol.operation.State = "interrupted"
	protocol.operation.Uploaded = api.Pointer(int64(2))
	fixture := newCLIManagementFixture(t, protocol.serve)
	stdout, stderr, err := fixture.run(t, []byte("exact original source"), "resume", "operation/"+sequencedOperationID, "-f", "-", "-o", "json")
	var operation api.Operation
	if !errors.Is(err, collection.ErrReselectionState) || json.Unmarshal([]byte(stdout), &operation) != nil || operation.ID != sequencedOperationID || operation.State != "interrupted" {
		t.Fatal("interruption lost original handle or classification", stdout, stderr, err)
	}
	if !strings.Contains(err.Error(), "validation was interrupted") || !strings.Contains(err.Error(), "retained evidence") || !strings.Contains(stderr, sequencedOperationID) || !reflect.DeepEqual(protocol.calls, []string{"get-original"}) {
		t.Fatal("interruption was hidden or caused new work", protocol.calls, stderr, err)
	}
}
