package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/httpauth"
	"github.com/ziad-hsn/cpra/internal/persistence"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func executionHTTPAdmit(t *testing.T, f *managementFixture, resources ...api.Resource) persistence.CollectionState {
	t.Helper()
	validationHTTPAuthority(t, f)
	op := validationHTTPOperation(t, f, resources...)
	if _, err := f.sdk.Operations.Validate(t.Context(), op.ID); err != nil {
		t.Fatal(err)
	}
	worker := validationHTTPWorker(t, f)
	validationHTTPResult(t, f, "/api/v2/operations/"+op.ID+"/validation")
	worker.BeginStop()
	if err := worker.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	head, ok, err := f.server.cfg.Store.CollectionGet(op.ID)
	if err != nil || !ok || head.Validation == nil || !head.Validation.HistorySealed || !head.Validation.Header.Valid {
		t.Fatal("missing original validated collection", err)
	}
	at := time.Now().UTC()
	authority, err := f.server.cfg.Store.ObserveOperatorAuthority(t.Context(), head.Actor, at)
	if err != nil {
		t.Fatal(err)
	}
	activation := persistence.CollectionActivation{ID: uuid.NewString(), Authority: authority, InputProgressDigest: head.ProgressDigest,
		ItemCount: head.ItemCount, ResultID: head.Validation.Header.ResultID, ResultDescriptor: head.Validation.Descriptor,
		PlanID: head.Plan.Header.PlanID, PlanDescriptor: head.Plan.Descriptor, CapabilitiesDigest: head.Validation.Header.CapabilitiesDigest,
		ValidationRequest: persistence.CollectionValidationRequestFenceFor(head), At: at}
	return executionHTTPSubmit(t, f, persistence.Command{Kind: "collection", At: at, Collection: &persistence.CollectionCommand{
		Action: "activation_admit", OperationID: head.ID, UploadID: head.UploadID, Activation: &activation, ActivationAuthority: &authority}})
}

func executionHTTPSubmit(t *testing.T, f *managementFixture, command persistence.Command) persistence.CollectionState {
	t.Helper()
	results, err := f.server.cfg.Store.Submit(t.Context(), []persistence.Command{command})
	if err != nil || len(results) != 1 || results[0].Err != nil || results[0].Collection == nil {
		t.Fatalf("collection fixture command failed: %+v %v", results, err)
	}
	return results[0].Collection.Clone()
}

func executionHTTPFinalize(t *testing.T, f *managementFixture, head persistence.CollectionState) persistence.CollectionState {
	t.Helper()
	binding := persistence.CollectionExecutionBinding{OperationID: head.ID, UploadID: head.UploadID, ActivationID: head.Activation.ID,
		PlanID: head.Plan.Header.PlanID, PlanDigest: head.Plan.Descriptor.Digest}
	return executionHTTPSubmit(t, f, persistence.Command{Kind: "collection_execute", At: time.Now().UTC(), CollectionExecute: &persistence.CollectionExecuteCommand{
		Action: "finalize", Binding: binding, Finalize: persistence.CollectionExecutionFinalizeFenceFor(head)}})
}

func executionHTTPPublish(t *testing.T, f *managementFixture, head persistence.CollectionState) persistence.CollectionState {
	t.Helper()
	for attempts := 0; !head.ExecutionResult.HistorySealed; attempts++ {
		if attempts > 16 {
			t.Fatal("execution result did not seal")
		}
		head = executionHTTPSubmit(t, f, persistence.Command{Kind: "collection_execute", At: time.Now().UTC(), CollectionExecute: &persistence.CollectionExecuteCommand{
			Action: "publish", Binding: head.ExecutionResult.Summary.Binding, Publication: &persistence.CollectionExecutionPublication{Published: head.ExecutionResult.Published}}})
	}
	return head
}

func executionHTTPFixture(t *testing.T, count int) (*managementFixture, persistence.CollectionState) {
	t.Helper()
	f := newManagementFixture(t, true)
	resources := make([]api.Resource, count)
	for i := range resources {
		resources[i] = preflightMonitor(fmt.Sprintf("service-%03d", i), "https://unused.example.test")
	}
	head := executionHTTPAdmit(t, f, resources...)
	if _, err := f.sdk.Operations.Cancel(t.Context(), head.ID); err != nil {
		t.Fatal(err)
	}
	head, _, err := f.server.cfg.Store.CollectionGet(head.ID)
	if err != nil {
		t.Fatal(err)
	}
	return f, executionHTTPPublish(t, f, executionHTTPFinalize(t, f, head))
}

func executionHTTPPage(t *testing.T, f *managementFixture, id string, opts cpra.ExecutionResultPageOptions) api.Operation {
	t.Helper()
	page, err := f.sdk.Operations.ExecutionResult(t.Context(), id, opts)
	if err != nil || page == nil {
		t.Fatal("execution result page", err)
	}
	return page.Data
}

func TestCollectionExecutionHTTPAvailabilityAndOriginalPages(t *testing.T) {
	f := newManagementFixture(t, true)
	var providerCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { providerCalls.Add(1) }))
	defer target.Close()
	secret := "PRIVATE-EXECUTION-HTTP-CANARY"
	head := executionHTTPAdmit(t, f, preflightMonitor("service", target.URL), managementResource("Credential", "secret", api.CredentialSpec{Value: &secret}))
	path := "/api/v2/operations/" + head.ID
	response, _ := f.request(t, http.MethodPost, path+"/activate", managementOperatorToken, nil, nil)
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatal("stopped fixture coordinator admitted public activation", response.StatusCode)
	}
	if _, err := f.sdk.Operations.Cancel(t.Context(), head.ID); err != nil {
		t.Fatal(err)
	}
	pending := executionHTTPPage(t, f, head.ID, cpra.ExecutionResultPageOptions{})
	if pending.State != "canceled" || pending.ExecutionResult == nil || pending.ExecutionResult.State != "pending" || len(pending.Items) != 0 || pending.NextCursor != "" {
		t.Fatal("canceled parent fabricated result availability", pending)
	}
	before := f.server.cfg.Store.Status().CommittedIndex
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	_, err := f.sdk.Operations.WaitExecutionResult(ctx, head.ID)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || f.server.cfg.Store.Status().CommittedIndex != before {
		t.Fatal("canceled result wait returned readiness or mutated work", err)
	}
	head, _, err = f.server.cfg.Store.CollectionGet(head.ID)
	if err != nil {
		t.Fatal(err)
	}
	head = executionHTTPFinalize(t, f, head)
	pending = executionHTTPPage(t, f, head.ID, cpra.ExecutionResultPageOptions{})
	if pending.ExecutionResult == nil || pending.ExecutionResult.State != "pending" || pending.ExecutionResult.Summary != nil || len(pending.Items) != 0 {
		t.Fatal("unsealed result exposed immutable rows", pending)
	}
	head = executionHTTPPublish(t, f, head)
	before = f.server.cfg.Store.Status().CommittedIndex
	first := executionHTTPPage(t, f, head.ID, cpra.ExecutionResultPageOptions{Limit: 1})
	if first.State != "canceled" || first.ExecutionResult == nil || first.ExecutionResult.State != "ready" || first.ExecutionResult.Summary == nil || first.ExecutionResult.Summary.ResultID != head.Activation.ID || first.ExecutionResult.Summary.Unattempted != 2 || len(first.Items) != 1 || first.NextCursor == "" {
		t.Fatal("sealed first page lost original state or result identity", first)
	}
	second := executionHTTPPage(t, f, head.ID, cpra.ExecutionResultPageOptions{Limit: 1, Cursor: first.NextCursor})
	repeated := executionHTTPPage(t, f, head.ID, cpra.ExecutionResultPageOptions{Limit: 1, Cursor: first.NextCursor})
	if !reflect.DeepEqual(second, repeated) || second.NextCursor != "" || !reflect.DeepEqual(first.ExecutionResult, second.ExecutionResult) {
		t.Fatal("cursor retry changed the original page")
	}
	for i, page := range []api.Operation{first, second} {
		row := page.Items[0]
		if row.InputOrdinal == nil || *row.InputOrdinal != int64(i+1) || row.PlanOrdinal == nil || row.Source == "" || row.CatalogDecision != "unattempted" || row.Committed == nil || *row.Committed || row.Applied != nil || row.ChildDisposition != nil {
			t.Fatal("result invented a catalog or controller decision", row)
		}
	}
	iterator := f.sdk.Operations.ExecutionItems(head.ID, cpra.ExecutionResultPageOptions{Limit: 1})
	var seen int
	for iterator.Next(t.Context()) {
		seen++
	}
	if iterator.Err() != nil || seen != 2 {
		t.Fatal("SDK iterator lost result rows", seen, iterator.Err())
	}
	ready, err := f.sdk.Operations.WaitExecutionResult(t.Context(), head.ID)
	if err != nil || ready.Data.ExecutionResult.State != "ready" || len(ready.Data.Items) != 2 {
		t.Fatal("SDK wait did not return sealed results", err)
	}
	listed, err := f.sdk.Operations.List(t.Context(), cpra.ListOptions{})
	if err != nil || len(listed.Data.Items) != 1 || listed.Data.Items[0].ExecutionResult == nil || listed.Data.Items[0].ExecutionResult.State != "ready" || len(listed.Data.Items[0].Items) != 0 {
		t.Fatal("SDK operation list rejected sealed metadata or exposed item rows", err)
	}
	response, raw := f.request(t, http.MethodGet, path+"?limit=1", managementOperatorToken, nil, nil)
	for _, private := range []string{secret, target.URL, "ciphertext", "source_fingerprint", "identity_key", "prepared", "payload"} {
		if bytes.Contains(raw, []byte(private)) {
			t.Fatal("result leaked private input", private)
		}
	}
	if response.StatusCode != http.StatusOK || providerCalls.Load() != 0 || f.server.cfg.Store.Status().CommittedIndex != before {
		t.Fatal("result read invoked a provider or mutated durable work")
	}
}

func TestCollectionExecutionHTTPAuthorizationCursorAndBounds(t *testing.T) {
	f, head := executionHTTPFixture(t, 3)
	const foreignToken = "foreign-execution-reader-012345678901234567890"
	hash, err := httpauth.HashToken(foreignToken)
	if err != nil {
		t.Fatal(err)
	}
	policy := f.authConfig
	policy.Principals = append(append([]httpauth.Principal(nil), policy.Principals...), httpauth.Principal{ID: "foreign", Role: httpauth.Operator, TokenSHA256: hash})
	if err := f.server.cfg.ManagementAuth.Replace(policy); err != nil {
		t.Fatal(err)
	}
	first := executionHTTPPage(t, f, head.ID, cpra.ExecutionResultPageOptions{Limit: 1})
	path := "/api/v2/operations/" + head.ID
	continuation := path + "?cursor=" + url.QueryEscape(first.NextCursor)
	for _, tc := range []struct {
		path, token string
		status      int
	}{
		{path, "", 401}, {path, managementLegacyToken, 404}, {path, managementReaderToken, 404}, {path, foreignToken, 404},
		{continuation, managementReaderToken, 410}, {continuation, foreignToken, 410},
		{continuation + "&limit=2", managementOperatorToken, 400},
		{strings.Replace(continuation, head.ID, "different-operation", 1), managementOperatorToken, 400},
		{"/api/v2/operations?cursor=" + url.QueryEscape(first.NextCursor), managementOperatorToken, 410},
		{path + "/validation?cursor=" + url.QueryEscape(first.NextCursor), managementOperatorToken, 410},
		{path + "?limit=0", managementOperatorToken, 400}, {path + "?limit=501", managementOperatorToken, 400},
		{path + "?limit=1&limit=1", managementOperatorToken, 400}, {path + "?monitorID=service", managementOperatorToken, 400},
		{path + "?cursor=private-invalid", managementOperatorToken, 400},
	} {
		response, raw := f.request(t, http.MethodGet, tc.path, tc.token, nil, nil)
		if response.StatusCode != tc.status {
			t.Fatalf("request %s: %d %s", tc.path, response.StatusCode, raw)
		}
		var problem api.Problem
		if json.Unmarshal(raw, &problem) != nil || bytes.Contains(raw, []byte(head.ID)) || bytes.Contains(raw, []byte("private-invalid")) {
			t.Fatal("error disclosed private identity or malformed query", string(raw))
		}
	}
	for _, limit := range []int{0, 500} {
		page := executionHTTPPage(t, f, head.ID, cpra.ExecutionResultPageOptions{Limit: limit})
		if len(page.Items) != 3 || page.NextCursor != "" {
			t.Fatal("valid page limit lost results")
		}
	}
	ordinary := operationListCredential(t, f, "ordinary")
	for _, token := range []string{managementOperatorToken, managementReaderToken, foreignToken} {
		response, raw := f.request(t, http.MethodGet, "/api/v2/operations/"+ordinary.OperationID+"?limit=1", token, nil, nil)
		var result api.Operation
		if response.StatusCode != http.StatusOK || api.DecodeResponse(raw, &result) != nil || result.ExecutionResult != nil || len(result.Items) != 1 {
			t.Fatal("ordinary receipt visibility changed", response.StatusCode)
		}
	}
}

func TestCollectionExecutionHTTPUnactivatedAndExpired(t *testing.T) {
	f := newManagementFixture(t, true)
	op, _ := collectionCancellationHTTPSetup(t, f, false)
	result := executionHTTPPage(t, f, op.ID, cpra.ExecutionResultPageOptions{})
	if result.ExecutionResult != nil || len(result.Items) != 0 {
		t.Fatal("unactivated collection invented an execution result")
	}
	if _, err := f.sdk.Operations.WaitExecutionResult(t.Context(), op.ID); !errors.Is(err, cpra.ErrExecutionResultUnsupported) {
		t.Fatal("unactivated wait did not return unsupported", err)
	}
	f, head := executionHTTPFixture(t, 2)
	f.server.managementHTTP.now = func() time.Time { return head.ExecutionResult.Summary.FinalizedAt.AddDate(0, 0, 30) }
	result = executionHTTPPage(t, f, head.ID, cpra.ExecutionResultPageOptions{})
	if result.ExecutionResult == nil || result.ExecutionResult.State != "expired" || len(result.Items) != 0 || result.NextCursor != "" {
		t.Fatal("expired result returned rows or fabricated readiness")
	}
	if _, err := f.sdk.Operations.WaitExecutionResult(t.Context(), head.ID); !errors.Is(err, cpra.ErrExecutionResultExpired) {
		t.Fatal("expired result wait lost explicit expiry", err)
	}
}

func TestCollectionExecutionHTTPCommittedAndAppliedRemainDistinct(t *testing.T) {
	f := newManagementFixture(t, true)
	var providerCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { providerCalls.Add(1) }))
	defer target.Close()
	head := executionHTTPAdmit(t, f, preflightMonitor("service", target.URL), managementResource("Credential", "secret", api.CredentialSpec{Value: api.Pointer("PRIVATE-EXECUTED-VALUE")}))
	worker, err := f.catalog.StartCollectionExecutionCoordinator(t.Context(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		worker.BeginStop()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := worker.Wait(ctx); err != nil {
			t.Error(err)
		}
	})
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		head, _, err = f.server.cfg.Store.CollectionGet(head.ID)
		if err != nil {
			t.Fatal(err)
		}
		if head.Execution != nil && head.Execution.Processed == 2 {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("private executor did not commit the original decisions", worker.Status())
		case <-worker.Done():
			t.Fatal("private executor stopped", worker.Err())
		case <-tick.C:
		}
	}
	worker.BeginStop()
	if err := worker.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	pending := executionHTTPPage(t, f, head.ID, cpra.ExecutionResultPageOptions{})
	if !admissionCountIs(pending.Committed, 2) || !admissionCountIs(pending.Applied, 0) || pending.ExecutionResult == nil || pending.ExecutionResult.Counts == nil || pending.ExecutionResult.Counts.ChildPending != 2 || len(pending.Items) != 0 {
		t.Fatal("live child work was reported as applied", pending)
	}
	view, err := f.server.cfg.Store.OperationSnapshot(time.Now().UTC(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	children, _, err := view.Page(t.Context(), "", "", 100)
	if err != nil || len(children) != 2 {
		t.Fatal("missing accepted original child receipts", err)
	}
	for _, child := range children {
		if err := f.catalog.CompleteOperation(t.Context(), child.ID, child.Key.Kind == "Credential"); err != nil {
			t.Fatal(err)
		}
	}
	head, _, err = f.server.cfg.Store.CollectionGet(head.ID)
	if err != nil {
		t.Fatal(err)
	}
	head = executionHTTPPublish(t, f, executionHTTPFinalize(t, f, head))
	result := executionHTTPPage(t, f, head.ID, cpra.ExecutionResultPageOptions{})
	if result.State != "partial" || !admissionCountIs(result.Committed, 2) || !admissionCountIs(result.Applied, 1) || result.ExecutionResult.Summary.ChildApplied != 1 || result.ExecutionResult.Summary.ChildFailed != 1 || len(result.Items) != 2 {
		t.Fatal("terminal parent conflated catalog and child decisions", result)
	}
	for _, item := range result.Items {
		if item.CatalogDecision != "accepted" || item.Committed == nil || !*item.Committed || item.ChildDisposition == nil || item.Applied == nil {
			t.Fatal("accepted item lost its original catalog mutation", item)
		}
		if item.Kind == "Monitor" && (*item.Applied || item.ChildDisposition.State != "failed" || item.ChildDisposition.Outcome != "projection_failed") {
			t.Fatal("failed child changed the successful catalog decision", item)
		}
		if item.Kind == "Credential" && (!*item.Applied || item.ChildDisposition.State != "completed" || item.ChildDisposition.Outcome != "applied") {
			t.Fatal("applied child lost its terminal disposition", item)
		}
	}
	if providerCalls.Load() != 0 {
		t.Fatal("metadata execution fixture invoked a provider")
	}
}
