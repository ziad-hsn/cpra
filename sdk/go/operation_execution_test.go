package cpra

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func executionOperationFixture() api.Operation {
	at := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	s := api.ExecutionResultSummary{ResultID: "11111111-1111-4111-8111-111111111111", UploadID: "22222222-2222-4222-8222-222222222222", PlanID: "33333333-3333-4333-8333-333333333333", PlanDigest: strings.Repeat("b", 64), Digest: strings.Repeat("c", 64), Outcome: "partial", ItemCount: 1, Processed: 1, Accepted: 1, ChildFailed: 1, FinalizedAt: at, ExpiresAt: at.Add(30 * 24 * time.Hour), Bytes: 500}
	item := api.ApplyResult{ID: "Monitor/service", Kind: "Monitor", Outcome: "accepted", CatalogDecision: "accepted", InputOrdinal: ptr(int64(1)), PlanOrdinal: ptr(int64(1)), Source: "source.00000000000000000001", SourceDocument: ptr(int64(2)), SourceItem: ptr(int64(3)), OriginalUID: "original-uid", OldVersion: "old", UID: "original-uid", NewVersion: "new", Generation: ptr(int64(2)), CommittedIndex: ptr(int64(11)), DecidedAt: &at, Committed: ptr(true), Applied: ptr(false), ChildDisposition: &api.ExecutionChildDisposition{OperationID: "original-child", State: "failed", Outcome: "projection_failed", UpdatedAt: &at}}
	return api.Operation{ID: "original-parent", State: "partial", ContentDigest: strings.Repeat("a", 64), IdentityFormat: "cpra.collection.hmac-sha256-json-bytes.v1", ItemCount: ptr(int64(1)), Committed: ptr(int64(1)), Applied: ptr(int64(0)), ExecutionResult: &api.ExecutionResultAvailability{State: "ready", Summary: &s}, Items: []api.ApplyResult{item}}
}
func TestExecutionResultCatalogDecisionAndChildDisposition(t *testing.T) {
	operation := executionOperationFixture()
	client := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/operations/original-parent" || r.URL.Query().Get("limit") != "100" {
			t.Error("execution reader changed request", r.URL)
		}
		_ = json.NewEncoder(w).Encode(operation)
	}, nil)
	response, err := client.Operations.ExecutionResult(context.Background(), operation.ID, ExecutionResultPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	item := response.Data.Items[0]
	if item.CatalogDecision != "accepted" || item.ChildDisposition.Outcome != "projection_failed" || item.Committed == nil || !*item.Committed || item.Applied == nil || *item.Applied || response.Data.ExecutionResult.Summary.Accepted != 1 || response.Data.ExecutionResult.Summary.ChildFailed != 1 {
		t.Fatal("catalog commit was conflated with child failure")
	}
	if *item.InputOrdinal != 1 || *item.PlanOrdinal != 1 || item.Source != "source.00000000000000000001" || *item.SourceDocument != 2 || *item.SourceItem != 3 || item.OriginalUID != "original-uid" {
		t.Fatal("original identities lost")
	}
}
func TestWaitExecutionResultAvailability(t *testing.T) {
	for _, test := range []struct {
		name, state string
		absent      bool
		want        error
	}{{"ready", "ready", false, nil}, {"expired", "expired", false, ErrExecutionResultExpired}, {"future", "future-availability", false, ErrExecutionResultUnsupported}, {"absent", "", true, ErrExecutionResultUnsupported}, {"canceled-pending", "pending", false, context.DeadlineExceeded}} {
		t.Run(test.name, func(t *testing.T) {
			operation := executionOperationFixture()
			operation.State = "canceled"
			if test.absent {
				operation.ExecutionResult = nil
				operation.Items = nil
			} else {
				operation.ExecutionResult.State = test.state
				if test.state != "ready" {
					operation.Items = nil
					operation.ExecutionResult.Summary = nil
				}
			}
			var requests, mutations atomic.Int32
			client := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodGet {
					mutations.Add(1)
				}
				w.Header().Set("Retry-After", "30")
				_ = json.NewEncoder(w).Encode(operation)
			}, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			response, err := client.Operations.WaitExecutionResult(ctx, operation.ID)
			if !errors.Is(err, test.want) {
				t.Fatalf("wait error = %v, want %v", err, test.want)
			}
			if response == nil || response.Data.ID != operation.ID || response.OperationID != operation.ID || response.Data.State != "canceled" || requests.Load() != 1 || mutations.Load() != 0 {
				t.Fatal("wait lost original observation or mutated server work")
			}
		})
	}
}
func TestExecutionResultNotFoundRemainsTyped(t *testing.T) {
	client := fixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"code":"notFound"}`))
	}, nil)
	r, err := client.Operations.WaitExecutionResult(context.Background(), "original-parent")
	var problem *Error
	if !errors.As(err, &problem) || problem.StatusCode != 404 || errors.Is(err, ErrExecutionResultExpired) || r == nil || r.OperationID != "original-parent" {
		t.Fatal("missing operation was converted to result expiry", err)
	}
}
func TestExecutionResultUnknownObservationsRemainReadable(t *testing.T) {
	operation := executionOperationFixture()
	operation.ExecutionResult.Summary.Outcome = "future-parent"
	operation.Items[0].CatalogDecision = "future-decision"
	operation.Items[0].Outcome = "future-outcome"
	operation.Items[0].ChildDisposition.Outcome = "future-child-outcome"
	if err := api.ValidateExecutionResult(operation); err != nil {
		t.Fatal(err)
	}
	client := fixture(t, func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(operation) }, nil)
	r, err := client.Operations.ExecutionResult(context.Background(), operation.ID, ExecutionResultPageOptions{})
	if err != nil || r.Data.Items[0].CatalogDecision != "future-decision" || r.Data.Items[0].ChildDisposition.Outcome != "future-child-outcome" {
		t.Fatal("future observations were rewritten", err)
	}
}
func TestExecutionResultRejectsMalformedObservations(t *testing.T) {
	for name, change := range map[string]func(*api.Operation){
		"ready-without-summary": func(o *api.Operation) { o.ExecutionResult.Summary = nil },
		"conflicting-counts":    func(o *api.Operation) { o.ExecutionResult.Summary.Accepted = 0 },
		"pending-child-on-sealed-page": func(o *api.Operation) {
			o.Items[0].ChildDisposition = &api.ExecutionChildDisposition{OperationID: "child", State: "pending"}
			o.Items[0].Applied = nil
		},
		"unchanged-with-child":  func(o *api.Operation) { o.Items[0].CatalogDecision = "unchanged"; o.Items[0].Committed = ptr(false) },
		"applied-without-child": func(o *api.Operation) { o.Items[0].ChildDisposition = nil },
		"wrong-input":           func(o *api.Operation) { o.Items[0].InputOrdinal = ptr(int64(0)) },
		"private-source-path":   func(o *api.Operation) { o.Items[0].Source = "/private/config.yml" },
		"unattempted-with-decision": func(o *api.Operation) {
			o.Items[0].CatalogDecision = "unattempted"
			o.Items[0].ChildDisposition = nil
			o.Items[0].Applied = nil
			o.Items[0].Committed = ptr(false)
		},
	} {
		t.Run(name, func(t *testing.T) {
			o := executionOperationFixture()
			change(&o)
			if api.ValidateExecutionResult(o) == nil {
				t.Fatal("malformed execution observation accepted")
			}
		})
	}
	for _, field := range []string{"executionResult", "inputOrdinal", "childDisposition", "applied"} {
		t.Run("null-"+field, func(t *testing.T) {
			raw, _ := json.Marshal(executionOperationFixture())
			var o map[string]any
			_ = json.Unmarshal(raw, &o)
			if field == "executionResult" {
				o[field] = nil
			} else {
				o["items"].([]any)[0].(map[string]any)[field] = nil
			}
			raw, _ = json.Marshal(o)
			var decoded api.Operation
			if api.DecodeResponse(raw, &decoded) == nil {
				t.Fatal("null execution observation accepted")
			}
		})
	}
}
func TestExecutionItemsPinsSummaryAndInputOrder(t *testing.T) {
	for _, scenario := range []string{"stable", "changed-summary", "changed-profile"} {
		t.Run(scenario, func(t *testing.T) {
			var calls int
			client := fixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				o := executionOperationFixture()
				*o.ItemCount = 2
				o.ExecutionResult.Summary.ItemCount = 2
				o.ExecutionResult.Summary.Processed = 2
				o.ExecutionResult.Summary.Accepted = 2
				o.ExecutionResult.Summary.ChildFailed = 2
				if calls == 1 {
					o.NextCursor = "next"
					o.Items[0].PlanOrdinal = ptr(int64(2))
				} else {
					if r.URL.Query().Get("cursor") != "next" {
						t.Error("original cursor not used")
					}
					o.Items[0].ID = "Monitor/second"
					o.Items[0].InputOrdinal = ptr(int64(2))
					if scenario == "changed-summary" {
						o.ExecutionResult.Summary.Bytes++
					}
					if scenario == "changed-profile" {
						o.NormalizationProfile = "cpra.file.base.v1"
					}
				}
				_ = json.NewEncoder(w).Encode(o)
			}, nil)
			it := client.Operations.ExecutionItems("original-parent", ExecutionResultPageOptions{Limit: 1})
			if !it.Next(context.Background()) || *it.Value().InputOrdinal != 1 || *it.Value().PlanOrdinal != 2 {
				t.Fatal("first input order lost", it.Err())
			}
			if scenario != "stable" {
				if it.Next(context.Background()) || it.Err() == nil {
					t.Fatal("mutable result summary accepted")
				}
			} else if !it.Next(context.Background()) || *it.Value().InputOrdinal != 2 || it.Next(context.Background()) || it.Err() != nil {
				t.Fatal("stable page iteration failed", it.Err())
			}
		})
	}
}
