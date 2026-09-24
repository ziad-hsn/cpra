package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
	"gopkg.in/yaml.v3"
)

type applyCLIProtocol struct {
	t         *testing.T
	mu        sync.Mutex
	calls     []string
	operation api.Operation
	items     []api.ApplyItem
	ambiguous bool
}

func (p *applyCLIProtocol) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer named-operator-token" {
		p.t.Error("apply lost authentication")
	}
	base := "/api/v2/operations/" + sequencedOperationID
	w.Header().Set("Content-Type", "application/json")
	at := time.Now().UTC()
	switch {
	case r.Method == "POST" && r.URL.Path == "/api/v2/collections/prepare":
		p.calls = append(p.calls, "prepare")
		var request api.CollectionPrepareRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			p.t.Error("invalid prepare")
		}
		_ = json.NewEncoder(w).Encode(api.CollectionAdmission{Ticket: "fixture-ticket", ExpiresAt: at.Add(time.Hour)})
	case r.Method == "POST" && r.URL.Path == "/api/v2/operations":
		p.calls = append(p.calls, "create")
		var request api.OperationCreateRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.AdmissionTicket != "fixture-ticket" {
			p.t.Error("invalid create")
		}
		p.operation = api.Operation{ID: sequencedOperationID, State: "staging", IdentityFormat: commitment.Format, ContentDigest: request.ContentDigest, ItemCount: api.Pointer(request.ItemCount), Uploaded: api.Pointer(int64(0)), Committed: api.Pointer(int64(0)), Applied: api.Pointer(int64(0))}
		w.WriteHeader(202)
		_ = json.NewEncoder(w).Encode(p.operation)
	case r.Method == "PUT" && r.URL.Path == base+"/items":
		p.calls = append(p.calls, "upload")
		var request api.UploadRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			p.t.Error("invalid upload")
		}
		p.items = append(p.items, request.Items...)
		p.operation.Uploaded = api.Pointer(int64(len(p.items)))
		_ = json.NewEncoder(w).Encode(p.operation)
	case r.Method == "POST" && r.URL.Path == base+"/validate":
		p.calls = append(p.calls, "validate")
		p.operation.State = "validating"
		w.WriteHeader(202)
		_ = json.NewEncoder(w).Encode(p.operation)
	case r.Method == "GET" && r.URL.Path == base+"/validation":
		p.calls = append(p.calls, "validation")
		page := api.ValidationResultPage{OperationID: sequencedOperationID, IdentityFormat: commitment.Format, ContentDigest: p.operation.ContentDigest, ItemCount: *p.operation.ItemCount,
			Summary: api.ValidationResultSummary{ResultID: "validation", Valid: true, Count: *p.operation.ItemCount, Digest: strings.Repeat("a", 64), CapabilitiesDigest: strings.Repeat("b", 64), PlanID: "plan", PlanDigest: strings.Repeat("c", 64), FinalizedAt: at, ExpiresAt: at.Add(time.Hour)}}
		for _, item := range p.items {
			page.Items = append(page.Items, api.ValidationResultItem{Ordinal: item.Ordinal, Kind: item.Resource.Kind, ID: item.Resource.Metadata.ID, Source: item.Source, SourceDocument: item.SourceDocument, SourceItem: item.SourceItem, Change: "create"})
		}
		_ = json.NewEncoder(w).Encode(page)
	case r.Method == "POST" && r.URL.Path == base+"/activate":
		p.calls = append(p.calls, "activate")
		raw, _ := io.ReadAll(r.Body)
		if len(raw) != 0 || r.URL.RawQuery != "" {
			p.t.Error("activation included a body or query")
		}
		p.operation.State = "applying"
		p.operation.ExecutionResult = &api.ExecutionResultAvailability{State: "pending"}
		if p.ambiguous {
			w.WriteHeader(503)
			_ = json.NewEncoder(w).Encode(api.Problem{Code: "unavailable"})
			return
		}
		_ = json.NewEncoder(w).Encode(p.operation)
	case r.Method == "GET" && r.URL.Path == base:
		p.calls = append(p.calls, "get")
		_ = json.NewEncoder(w).Encode(p.operation)
	default:
		p.t.Errorf("unexpected collection request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(404)
	}
}

func TestCollectionApplyMultipleFilesAndUncertainAdmission(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		t.Run(map[bool]string{false: "admission", true: "lost-activation-reply"}[ambiguous], func(t *testing.T) {
			spool := diffSpool(t)
			dir := t.TempDir()
			first := diffFile(t, dir, "one.json", cliResource("Monitor", "one"))
			second := diffFile(t, dir, "two.json", cliResource("Recipient", "two"))
			protocol := &applyCLIProtocol{t: t, ambiguous: ambiguous}
			fixture := newCLIManagementFixture(t, protocol.serve)
			stdout, stderr, err := fixture.run(t, nil, "apply", "-f", first, "-f", second, "-o", "json")
			var operation api.Operation
			if err != nil || json.Unmarshal([]byte(stdout), &operation) != nil || operation.ID != sequencedOperationID || operation.State != "applying" || *operation.Uploaded != 2 || !strings.Contains(stderr, sequencedOperationID) {
				t.Fatal("apply failed or lost original admission", stdout, stderr, err)
			}
			protocol.mu.Lock()
			defer protocol.mu.Unlock()
			want := []string{"prepare", "create", "upload", "validate", "validation", "activate"}
			if ambiguous {
				want = append(want, "get")
			}
			if !reflect.DeepEqual(protocol.calls, want) || len(protocol.items) != 2 || protocol.items[0].Source == protocol.items[1].Source {
				t.Fatal("apply changed inventory, duplicated mutation or waited implicitly", protocol.calls)
			}
			if strings.Contains(stdout+stderr, "identityKey") || strings.Contains(stdout+stderr, cliSecretValue) {
				t.Fatal("private input leaked")
			}
			assertDiffSpoolRemoved(t, spool)
		})
	}
}

func TestCollectionApplyDryRunAndInvalidInputsNeverAllocate(t *testing.T) {
	var calls atomic.Int32
	fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(diffResponse(t, r))
	})
	raw, _ := json.Marshal(cliResource("Monitor", "one"))
	stdout, stderr, err := fixture.run(t, raw, "apply", "-f", "-", "--dry-run=server", "-o", "json")
	if err != nil || stderr != "" || !strings.Contains(stdout, `"changed": true`) || calls.Load() != 1 {
		t.Fatal(stdout, stderr, err)
	}
	for _, args := range [][]string{{"--dry-run=client", "-f", "-"}, {"--dry-run=server", "--wait", "-f", "-"}, {"--timeout=1s", "-f", "-"}, {"--wait", "--timeout=-1s", "-f", "-"}, {"-f", "-", "-f", "-"}, {}} {
		_, _, err := fixture.run(t, raw, append([]string{"apply"}, args...)...)
		if err == nil {
			t.Fatal("invalid apply options accepted", args)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("invalid inputs allocated or reached preflight")
	}
}

func TestCollectionApplyWaitCancellationPreservesAdmission(t *testing.T) {
	protocol := &applyCLIProtocol{t: t}
	fixture := newCLIManagementFixture(t, protocol.serve)
	raw, _ := json.Marshal(cliResource("Monitor", "one"))
	stdout, stderr, err := fixture.run(t, raw, "apply", "-f", "-", "--wait", "--timeout=50ms", "-o", "json")
	var operation api.Operation
	if !errors.Is(err, context.DeadlineExceeded) || json.Unmarshal([]byte(stdout), &operation) != nil || operation.ID != sequencedOperationID || operation.State != "applying" || *operation.Uploaded != 1 || !strings.Contains(stderr, sequencedOperationID) {
		t.Fatal(stdout, stderr, err)
	}
	protocol.mu.Lock()
	defer protocol.mu.Unlock()
	if !reflect.DeepEqual(protocol.calls, []string{"prepare", "create", "upload", "validate", "validation", "activate", "get"}) {
		t.Fatal("timeout canceled or resubmitted work", protocol.calls)
	}
}

func TestCollectionWaitReadOnlyPartialResultFormats(t *testing.T) {
	for _, output := range []string{"json", "yaml", "table", "wide"} {
		t.Run(output, func(t *testing.T) {
			page := cliExecutionPage()
			var calls atomic.Int32
			fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/api/v2/operations/"+sequencedOperationID || r.URL.Query().Get("cursor") != "" {
					t.Error("wait mutated or changed original handle")
				}
				if calls.Add(1) == 2 && r.URL.Query().Get("limit") != "100" {
					t.Error("wait did not request bounded first page")
				}
				_ = json.NewEncoder(w).Encode(page)
			})
			stdout, stderr, err := fixture.run(t, nil, "wait", "operation/"+sequencedOperationID, "-o", output)
			if err == nil || !strings.Contains(err.Error(), "did not complete successfully") || calls.Load() != 2 {
				t.Fatal("partial execution reported success or collected pages", err, calls.Load())
			}
			if output == "table" || output == "wide" {
				if !strings.Contains(stdout, "Failed children:") || !strings.Contains(stdout, "projection_failed") || stderr != "Next cursor: next-page\n" {
					t.Fatal(stdout, stderr)
				}
				return
			}
			var object map[string]any
			raw := []byte(stdout)
			if output == "yaml" {
				if yaml.Unmarshal(raw, &object) != nil {
					t.Fatal("invalid YAML")
				}
				raw, _ = json.Marshal(object)
			}
			var result api.Operation
			if api.DecodeResponse(raw, &result) != nil || !reflect.DeepEqual(result, page) || stderr != "" {
				t.Fatal("wait changed canonical partial result", stdout, stderr)
			}
		})
	}
}

func TestCollectionWaitCancellationDoesNotCancelServer(t *testing.T) {
	page := cliExecutionPage()
	page.Items = nil
	page.NextCursor = ""
	page.ExecutionResult = &api.ExecutionResultAvailability{State: "pending", Counts: &api.ExecutionResultCounts{Processed: 1, Accepted: 1, ChildPending: 1}}
	var calls atomic.Int32
	fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "GET" {
			t.Error("wait mutated")
		}
		_ = json.NewEncoder(w).Encode(page)
	})
	stdout, _, err := fixture.run(t, nil, "wait", "operation", sequencedOperationID, "--timeout=50ms", "-o", "json")
	var result api.Operation
	if !errors.Is(err, context.DeadlineExceeded) || json.Unmarshal([]byte(stdout), &result) != nil || result.ID != page.ID || result.ExecutionResult.Counts.ChildPending != 1 || calls.Load() != 2 {
		t.Fatal(stdout, err, calls.Load())
	}
}

func TestCollectionWaitCanceledBeforeRequestAndInvalidHandle(t *testing.T) {
	var calls atomic.Int32
	fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	for _, args := range [][]string{{"wait", sequencedOperationID}, {"wait", "operation/not-a-handle"}, {"wait", "operation/" + sequencedOperationID, "--timeout=-1s"}} {
		if _, _, err := fixture.run(t, nil, args...); err == nil {
			t.Fatal("invalid wait accepted")
		}
	}
	root := NewRootCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"--server", fixture.server.URL, "--ca-file", fixture.ca, "--token-file", fixture.token, "wait", "operation/" + sequencedOperationID})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := root.ExecuteContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("invalid/canceled wait made a request")
	}
}

func TestCollectionApplyWaitUnsupportedObservationRetainsOriginal(t *testing.T) {
	page := cliExecutionPage()
	page.ExecutionResult = &api.ExecutionResultAvailability{State: "futureAvailability"}
	page.Items = nil
	page.NextCursor = ""
	fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(page) })
	stdout, _, err := fixture.run(t, nil, "wait", "operation/"+sequencedOperationID, "-o", "json")
	if !errors.Is(err, cpra.ErrExecutionResultUnsupported) || !strings.Contains(stdout, "futureAvailability") {
		t.Fatal(stdout, err)
	}
}

func TestCollectionWaitUnknownIdentityAndConflictingCompletionAreNotSuccess(t *testing.T) {
	for _, future := range []bool{false, true} {
		page := cliExecutionPage()
		page.State, page.ExecutionResult.Summary.Outcome = "completed", "completed"
		if future {
			page.IdentityFormat = "future.collection-format"
		}
		fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" {
				t.Error("unknown result triggered a mutation")
			}
			_ = json.NewEncoder(w).Encode(page)
		})
		stdout, _, err := fixture.run(t, nil, "wait", "operation/"+sequencedOperationID, "-o", "json")
		if err == nil || future && !errors.Is(err, cpra.ErrExecutionResultUnsupported) || !strings.Contains(stdout, `"childFailed": 1`) {
			t.Fatal("unconfirmed success replaced original failure counts", stdout, err)
		}
	}
}

func TestCollectionApplyMalformedFinalFilePreventsAllRequests(t *testing.T) {
	dir := t.TempDir()
	first := diffFile(t, dir, "first.json", cliResource("Monitor", "one"))
	var calls atomic.Int32
	fixture := newCLIManagementFixture(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
	stdout, stderr, err := fixture.run(t, []byte("spec: ["+cliSecretValue), "apply", "-f", first, "-f", "-")
	if err == nil || calls.Load() != 0 || stdout != "" || strings.Contains(stderr+err.Error(), cliSecretValue) {
		t.Fatal("malformed final source allocated or leaked input", stdout, stderr, err)
	}
}
