package cli

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"gopkg.in/yaml.v3"
)

func cliExecutionPage() api.Operation {
	at := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	summary := api.ExecutionResultSummary{ResultID: "11111111-1111-4111-8111-111111111111", UploadID: "22222222-2222-4222-8222-222222222222", PlanID: "33333333-3333-4333-8333-333333333333",
		PlanDigest: strings.Repeat("b", 64), Digest: strings.Repeat("c", 64), Outcome: "canceled", ItemCount: 2, Processed: 1, Accepted: 1, Unattempted: 1, ChildFailed: 1,
		FinalizedAt: at, ExpiresAt: at.AddDate(0, 0, 30), Bytes: 1000}
	row := api.ApplyResult{ID: "Monitor/service", Kind: "Monitor", Outcome: "accepted", CatalogDecision: "accepted", InputOrdinal: api.Pointer(int64(1)), PlanOrdinal: api.Pointer(int64(2)),
		Source: "source.00000000000000000001", SourceDocument: api.Pointer(int64(2)), SourceItem: api.Pointer(int64(3)), OriginalUID: "original-uid", OldVersion: "old", UID: "original-uid", NewVersion: "new",
		Generation: api.Pointer(int64(2)), CommittedIndex: api.Pointer(int64(11)), DecidedAt: &at, Committed: api.Pointer(true), Applied: api.Pointer(false),
		ChildDisposition: &api.ExecutionChildDisposition{OperationID: cliOperationID, State: "failed", Outcome: "projection_failed", UpdatedAt: &at}}
	return api.Operation{ID: sequencedOperationID, State: "canceled", IdentityFormat: "cpra.collection.hmac-sha256-json-bytes.v1", ContentDigest: strings.Repeat("a", 64), ItemCount: api.Pointer(int64(2)),
		Committed: api.Pointer(int64(1)), Applied: api.Pointer(int64(0)), ExecutionResult: &api.ExecutionResultAvailability{State: "ready", Summary: &summary}, Items: []api.ApplyResult{row}, NextCursor: "next-page"}
}

func TestManagementExecutionResultsUseOneExactSDKPage(t *testing.T) {
	for _, output := range []string{"table", "wide", "json", "yaml"} {
		t.Run(output, func(t *testing.T) {
			page := cliExecutionPage()
			var requests atomic.Int32
			fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/api/v2/operations/"+sequencedOperationID || r.URL.Query().Get("limit") != "1" || r.URL.Query().Get("cursor") != "" || len(r.URL.Query()) != 1 || r.Header.Get("Authorization") != "Bearer named-operator-token" {
					t.Error("result read changed the original operation, bounds or authentication")
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Operation-ID", sequencedOperationID)
				_ = json.NewEncoder(w).Encode(page)
			})
			stdout, stderr, err := fixture.run(t, nil, "get", "operation", sequencedOperationID, "--results", "--limit", "1", "-o", output)
			if err != nil || requests.Load() != 1 {
				t.Fatal("result read failed, retried or collected another page", requests.Load(), err)
			}
			if output == "table" || output == "wide" {
				for _, expected := range []string{"Execution results:", "ready", "Accepted:", "Failed children:", "CONFIGURATION DECISION", "CONTROLLER STATE", "CONTROLLER OUTCOME", "projection_failed"} {
					if !strings.Contains(stdout, expected) {
						t.Fatal("result table lost an observation", expected, stdout)
					}
				}
				found := false
				for _, line := range strings.Split(stdout, "\n") {
					fields := strings.Fields(line)
					if len(fields) >= 7 && fields[1] == "Monitor/service" {
						found = strings.Join(fields[:7], ",") == "1,Monitor/service,accepted,yes,failed,projection_failed,no"
					}
				}
				if !found || stderr != "Next cursor: next-page\n" || strings.Contains(stdout, "Next cursor:") {
					t.Fatal("result table conflated committed/applied or lost continuation", stdout, stderr)
				}
				return
			}
			var decoded api.Operation
			raw := []byte(stdout)
			if output == "yaml" {
				var object map[string]any
				if err := yaml.Unmarshal(raw, &object); err != nil {
					t.Fatal(err)
				}
				raw, err = json.Marshal(object)
				if err != nil {
					t.Fatal(err)
				}
			}
			if api.DecodeResponse(raw, &decoded) != nil || !reflect.DeepEqual(decoded, page) || stderr != "" {
				t.Fatal("structured output changed the canonical typed result page", stdout, stderr)
			}
		})
	}
}

func TestManagementExecutionResultsForwardContinuationAndDefaultLimit(t *testing.T) {
	for _, cursor := range []string{"", "original/+?cursor"} {
		t.Run(cursor, func(t *testing.T) {
			page := cliExecutionPage()
			if cursor != "" {
				row := page.Items[0]
				row.ID, row.Kind, row.Outcome, row.CatalogDecision = "Credential/untouched", "Credential", "unattempted", "unattempted"
				row.InputOrdinal, row.PlanOrdinal = api.Pointer(int64(2)), api.Pointer(int64(1))
				row.Committed, row.Applied, row.ChildDisposition = api.Pointer(false), nil, nil
				row.UID, row.NewVersion, row.OriginalUID, row.OldVersion = "", "", "", ""
				row.Generation, row.CommittedIndex, row.DecidedAt = nil, nil, nil
				page.Items, page.NextCursor = []api.ApplyResult{row}, ""
			}
			var requests atomic.Int32
			fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/api/v2/operations/"+sequencedOperationID || r.URL.Query().Get("limit") != "100" || r.URL.Query().Get("cursor") != cursor {
					t.Error("result continuation changed the cursor or limit")
				}
				_ = json.NewEncoder(w).Encode(page)
			})
			args := []string{"get", "operations", sequencedOperationID, "--results", "-o", "json"}
			if cursor != "" {
				args = append(args, "--cursor", cursor)
			}
			stdout, stderr, err := fixture.run(t, nil, args...)
			var decoded api.Operation
			if err != nil || requests.Load() != 1 || api.DecodeResponse([]byte(stdout), &decoded) != nil || !reflect.DeepEqual(decoded, page) || stderr != "" {
				t.Fatal("result continuation fell back or fabricated missing progress", requests.Load(), err, stdout, stderr)
			}
		})
	}
}

func TestManagementExecutionResultsRejectInvalidFlagsBeforeRequests(t *testing.T) {
	var requests atomic.Int32
	fixture := newCLIManagementFixture(t, func(http.ResponseWriter, *http.Request) { requests.Add(1) })
	for _, args := range [][]string{
		{"get", "operation", "--results"}, {"get", "operations", "--results", "--cursor", "old"},
		{"get", "operation", "not-an-operation", "--results"},
		{"get", "operation", sequencedOperationID, "--results", "--limit", "0"},
		{"get", "operation", sequencedOperationID, "--results", "--limit", "501"},
		{"get", "operation", sequencedOperationID, "--results", "--monitor-id", "service"},
		{"get", "operation", sequencedOperationID, "--results", "--monitor-id", ""},
		{"get", "operation", sequencedOperationID, "--results", "--wait"},
		{"get", "operation", sequencedOperationID, "--results", "--all"},
		{"get", "operation", sequencedOperationID, "--results", "--selector", "team=infra"},
		{"get", "operation", sequencedOperationID, "--results", "--cursor", strings.Repeat("x", 4097)},
		{"get", "operation", sequencedOperationID, "--results=false", "--cursor", "ignored"},
	} {
		if stdout, _, err := fixture.run(t, nil, args...); err == nil || stdout != "" {
			t.Fatal("invalid result flags accepted", args)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("invalid result flags reached the server")
	}
}

func TestManagementExecutionResultsPreserveAvailabilityAndFutureObservations(t *testing.T) {
	for _, state := range []string{"", "pending", "expired", "future-availability", "ready"} {
		t.Run(state, func(t *testing.T) {
			page := cliExecutionPage()
			if state == "" {
				page.ExecutionResult, page.Items, page.NextCursor = nil, nil, ""
			} else if state != "ready" {
				page.ExecutionResult = &api.ExecutionResultAvailability{State: state}
				page.Items, page.NextCursor = nil, ""
			} else {
				page.State, page.ExecutionResult.Summary.Outcome = "future-parent", "future-parent"
				page.Items[0].CatalogDecision = "future-decision"
				page.Items[0].Outcome = "future-outcome"
				page.Items[0].ChildDisposition.State = "future-child"
				page.Items[0].ChildDisposition.Outcome = "future-disposition"
			}
			var requests atomic.Int32
			fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodGet {
					t.Error("result observation made a mutation")
				}
				w.Header().Set("Retry-After", "30")
				_ = json.NewEncoder(w).Encode(page)
			})
			stdout, _, err := fixture.run(t, nil, "get", "operation", sequencedOperationID, "--results")
			if err != nil || requests.Load() != 1 {
				t.Fatal("result observation polled, mutated or rejected future vocabulary", err)
			}
			if state == "ready" {
				for _, value := range []string{"future-parent", "future-decision", "future-child", "future-disposition"} {
					if !strings.Contains(stdout, value) {
						t.Fatal("unknown observation was rewritten", value, stdout)
					}
				}
			} else {
				want := state
				if want == "" {
					want = "unavailable"
				}
				if !strings.Contains(stdout, want) || strings.Contains(stdout, "CONFIGURATION DECISION") {
					t.Fatal("availability was rewritten or fabricated rows", stdout)
				}
			}
		})
	}
}

func TestManagementExecutionResultsErrorsRemainTypedAndRedacted(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"denied", 403, `{"status":403,"detail":"PRIVATE-ERROR-DETAIL"}`, cpra.ErrUnauthorized},
		{"expired", 410, `{"status":410,"code":"cursorExpired","detail":"PRIVATE-ERROR-DETAIL"}`, cpra.ErrExpired},
		{"unavailable", 503, `{"status":503,"detail":"PRIVATE-ERROR-DETAIL"}`, cpra.ErrUnavailable},
		{"malformed", 200, `{"id":"` + sequencedOperationID + `","state":"canceled","contentDigest":"` + strings.Repeat("a", 64) + `","executionResult":null}`, nil},
		{"oversized", 200, `{"padding":"` + strings.Repeat("a", 4<<20) + `"}`, cpra.ErrResponseTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			})
			stdout, stderr, err := fixture.run(t, nil, "get", "operation", sequencedOperationID, "--results")
			if err == nil || tc.want != nil && !errors.Is(err, tc.want) || requests.Load() != 1 || stdout != "" || strings.Contains(stderr+err.Error(), "PRIVATE-ERROR-DETAIL") {
				t.Fatal("result error retried, fell back, lost its type or exposed detail", requests.Load(), err)
			}
		})
	}
}

func TestManagementExecutionResultsDistinguishRestoreInvalidation(t *testing.T) {
	page := cliExecutionPage()
	page.State, page.NextCursor = "partial", ""
	page.Committed = api.Pointer(int64(2))
	summary := page.ExecutionResult.Summary
	summary.Outcome, summary.Processed, summary.Accepted = "partial", 2, 2
	summary.Unattempted, summary.ChildFailed = 0, 0
	summary.ChildSuperseded, summary.ChildInvalidated = 1, 1
	page.Items[0].ChildDisposition.State, page.Items[0].ChildDisposition.Outcome = "partial", "superseded"
	page.Items[0].ChildDisposition.InvalidatedByRestore = "restore-original-id"
	second := page.Items[0]
	second.ID = "Monitor/ordinary-supersession"
	second.InputOrdinal, second.PlanOrdinal = api.Pointer(int64(2)), api.Pointer(int64(1))
	child := *second.ChildDisposition
	child.InvalidatedByRestore = ""
	second.ChildDisposition = &child
	page.Items = append(page.Items, second)
	fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(page) })
	for _, output := range []string{"table", "wide"} {
		stdout, _, err := fixture.run(t, nil, "get", "operation", sequencedOperationID, "--results", "-o", output)
		if err != nil || !strings.Contains(stdout, "RESTORE INVALIDATED") || output == "wide" && !strings.Contains(stdout, "restore-original-id") {
			t.Fatal("restore disposition disappeared from result table", err, stdout)
		}
		seen := 0
		for _, line := range strings.Split(stdout, "\n") {
			fields := strings.Fields(line)
			if len(fields) < 8 {
				continue
			}
			switch fields[1] {
			case "Monitor/service":
				if fields[7] != "yes" {
					t.Fatal("restore invalidation became ordinary supersession", line)
				}
				seen++
			case "Monitor/ordinary-supersession":
				if fields[7] != "no" {
					t.Fatal("ordinary supersession became restore invalidation", line)
				}
				seen++
			}
		}
		if seen != 2 {
			t.Fatal("result table omitted child dispositions")
		}
	}
}
