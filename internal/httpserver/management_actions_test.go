package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func unknownControlAction(t *testing.T, f *managementFixture, id string, local bool) (persistence.Monitor, persistence.Action, *persistence.LocalExecution) {
	t.Helper()
	_, m, g := configureControlMonitor(t, f, id)
	m = controlPulse(t, f, m, g, "failure")
	var a persistence.Action
	for _, candidate := range m.Actions {
		if candidate.Kind == "code" {
			a = candidate
			break
		}
	}
	if a.ID == "" {
		t.Fatal("no notification intent")
	}
	start := persistence.Command{Kind: "start", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: time.Now().UTC(), Guard: &g}
	var handle *persistence.LocalExecution
	if local {
		var err error
		handle, err = f.server.cfg.Store.BeginLocalAction(context.Background(), start)
		if handle != nil {
			t.Cleanup(func() {
				if err := handle.Finish(context.Background()); err != nil {
					t.Error(err)
				}
			})
		}
		if err != nil {
			t.Fatal(err)
		}
	} else {
		results, err := f.server.cfg.Store.Submit(context.Background(), []persistence.Command{start})
		if err != nil || len(results) != 1 || !results[0].Allowed {
			t.Fatalf("start: %+v %v", results, err)
		}
	}
	results, err := f.server.cfg.Store.Submit(context.Background(), []persistence.Command{{Kind: "result", MonitorID: m.ID, Revision: m.Revision, ActionID: a.ID, At: time.Now().UTC(), Outcome: "failure", Ambiguous: true}})
	if err != nil || len(results) != 1 || results[0].Err != nil || results[0].Monitor == nil || results[0].Monitor.Actions[a.ID].State != persistence.Unknown {
		t.Fatalf("unknown: %+v %v", results, err)
	}
	return *results[0].Monitor, results[0].Monitor.Actions[a.ID], handle
}
func TestManagementActionReviewRequiresActualExecutorFence(t *testing.T) {
	f := newManagementFixture(t, true)
	m, a, handle := unknownControlAction(t, f, "service", true)
	ctx := context.Background()
	read, err := f.sdk.Actions.Get(ctx, a.ID)
	if err != nil || !read.Data.Held || read.Data.ExecutorFenced || read.Data.State != "unknown" || read.ResourceVersion != read.Data.ReviewRevision {
		t.Fatalf("read: %+v %v", read, err)
	}
	req := api.ControlRequest{Revision: read.Data.ReviewRevision, Resolution: "accepted", Reason: "Provider confirms delivery", Note: "Checked recipient receipt", EvidenceRefs: []string{"ticket:incident-123"}}
	_, err = f.sdk.Actions.Review(ctx, a.ID, req)
	varProblem(t, err, 409, "executorUnfenced")
	if err := handle.Finish(ctx); err != nil {
		t.Fatal(err)
	}
	latest, err := f.sdk.Actions.Get(ctx, a.ID)
	if err != nil || !latest.Data.ExecutorFenced || latest.Data.ReviewRevision == req.Revision {
		t.Fatalf("fence version: %+v %v", latest, err)
	}
	_, err = f.sdk.Actions.Review(ctx, a.ID, req)
	varProblem(t, err, 412, "versionConflict")
	req.Revision = latest.Data.ReviewRevision
	reviewed, err := f.sdk.Actions.Review(ctx, a.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if reviewed.OperationID == "" || reviewed.Data.State != "unknown" || reviewed.Data.Outcome != "external_outcome_unknown" || reviewed.Data.Held || reviewed.Data.Review == nil || reviewed.Data.Review.Actor != "operator" || reviewed.Data.Review.Resolution != api.Accepted || reviewed.Data.Review.EvidenceRefs[0] != "ticket:incident-123" {
		t.Fatalf("review changed facts or lost audit: %+v", reviewed)
	}
	operation, err := f.sdk.Operations.Get(ctx, reviewed.OperationID)
	if err != nil || (operation.Data.Applied == nil || *operation.Data.Applied != 0) || operation.Data.State != "committed" {
		t.Fatalf("premature applied receipt: %+v %v", operation, err)
	}
	if err := f.catalog.CompleteOperation(ctx, reviewed.OperationID, true); err != nil {
		t.Fatal(err)
	}
	operation, err = f.sdk.Operations.Get(ctx, reviewed.OperationID)
	if err != nil || (operation.Data.Applied == nil || *operation.Data.Applied != 1) {
		t.Fatal("owner completion missing", err)
	}
	history, err := f.sdk.History(ctx, cpra.ListOptions{MonitorID: m.ID})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range history.Data.Items {
		if event.Kind == "action_reviewed" {
			found = event.ActionID == a.ID && event.IncidentID == a.IncidentID && event.Actor == "operator" && event.Outcome == "accepted" && event.Note == req.Note && len(event.EvidenceRefs) == 1 && event.EvidenceRefs[0] == req.EvidenceRefs[0]
		}
	}
	if !found {
		t.Fatal("review audit missing", history.Data.Items)
	}
	raw, _ := json.Marshal(reviewed.Data)
	response, wire := f.request(t, "GET", "/api/v2/actions/"+a.ID, managementOperatorToken, nil, nil)
	if response.StatusCode != 200 || !strings.Contains(string(wire), `"held":false`) || !strings.Contains(string(wire), `"executorFenced":true`) {
		t.Fatalf("known action booleans omitted: %s", wire)
	}
	for _, private := range []string{"executor_session", "provider-not-invoked.invalid", "policy\""} {
		if strings.Contains(string(raw), private) {
			t.Fatal("private internals exposed", private)
		}
	}
}

func TestManagementActionPaginationAndAdmission(t *testing.T) {
	f := newManagementFixture(t, true)
	_, a, _ := unknownControlAction(t, f, "first", false)
	_, other, _ := unknownControlAction(t, f, "second", false)
	ctx := context.Background()
	page, err := f.sdk.Actions.List(ctx, cpra.ListOptions{Limit: 1})
	if err != nil || len(page.Data.Items) != 1 || page.Data.NextCursor == "" {
		t.Fatalf("page: %+v %v", page, err)
	}
	path := "/api/v2/actions?limit=1&cursor=" + url.QueryEscape(page.Data.NextCursor)
	for _, test := range []struct {
		path, token string
		status      int
	}{{path, managementReaderToken, 410}, {path + "&monitorID=first", managementOperatorToken, 400}, {"/api/v2/actions?monitorID=first&limit=501", managementOperatorToken, 400}, {"/api/v2/actions?selector=team%3Dops", managementOperatorToken, 501}} {
		res, raw := f.request(t, "GET", test.path, test.token, nil, nil)
		if res.StatusCode != test.status {
			t.Fatalf("query %d %s", res.StatusCode, raw)
		}
	}
	filtered, err := f.sdk.Actions.List(ctx, cpra.ListOptions{MonitorID: "first"})
	if err != nil || len(filtered.Data.Items) != 1 || filtered.Data.Items[0].ID != a.ID {
		t.Fatalf("filter: %+v %v", filtered, err)
	}
	current, err := f.sdk.Actions.Get(ctx, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(api.ControlRequest{Revision: current.Data.ReviewRevision, Reason: "No independent receipt", Resolution: "inconclusive"})
	for _, test := range []struct {
		body        []byte
		token, etag string
		status      int
	}{{body, managementReaderToken, `"` + current.Data.ReviewRevision + `"`, 403}, {body, managementOperatorToken, current.Data.ReviewRevision, 400}, {body, managementOperatorToken, `W/"` + current.Data.ReviewRevision + `"`, 400}, {body, managementOperatorToken, `"different"`, 400}, {[]byte(`{"revision":"` + current.Data.ReviewRevision + `","resolution":"inconclusive","reason":"x","actor":"impostor"}`), managementOperatorToken, `"` + current.Data.ReviewRevision + `"`, 400}} {
		res, raw := f.request(t, "POST", "/api/v2/actions/"+other.ID+"/review", test.token, test.body, map[string]string{"If-Match": test.etag})
		if res.StatusCode != test.status {
			t.Fatalf("admission: %d %s", res.StatusCode, raw)
		}
	}
	reviewed, err := f.sdk.Actions.Review(ctx, other.ID, api.ControlRequest{Revision: current.Data.ReviewRevision, Reason: "No independent receipt", Resolution: "inconclusive"})
	if err != nil || !reviewed.Data.Held {
		t.Fatalf("inconclusive review: %+v %v", reviewed, err)
	}
	if err := f.server.StopAdmission(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = f.sdk.Actions.Review(ctx, other.ID, api.ControlRequest{Revision: reviewed.Data.ReviewRevision, Reason: "Still unclear", Resolution: "inconclusive"})
	varProblem(t, err, http.StatusServiceUnavailable, "admissionUnavailable")
	if _, err := f.sdk.Actions.Get(ctx, other.ID); err != nil {
		t.Fatal("diagnostics closed during drain", err)
	}
}
func varProblem(t *testing.T, err error, status int, code string) {
	t.Helper()
	var p *cpra.Error
	if !errors.As(err, &p) || p.Problem.Status != int64(status) || p.Problem.Code != code {
		t.Fatalf("want %d %s, got %T %v", status, code, err, err)
	}
}

func TestManagementRecoveryIsGuardedIntentWithPendingReceipt(t *testing.T) {
	f := newManagementFixture(t, true)
	_, m, g := configureControlMonitor(t, f, "service")
	ctx := context.Background()
	m.Policy.Intervention = true
	m.Policy.Unhealthy = 2
	m.Policy.RecoveryMaxAttempts = 3
	configured, err := f.server.cfg.Store.Submit(ctx, []persistence.Command{{Kind: "configure", MonitorID: m.ID, Revision: m.Revision, At: time.Now().UTC(), Config: &m, Guard: &g}})
	if err != nil || len(configured) != 1 || configured[0].Err != nil {
		t.Fatalf("configure recovery: %+v %v", configured, err)
	}
	m = controlPulse(t, f, *configured[0].Monitor, g, "failure")
	if len(m.Actions) != 0 {
		t.Fatal("automatic fixture unexpectedly queued recovery")
	}
	current, err := f.sdk.Monitors.Get(ctx, m.ID)
	if err != nil || current.Data.Status.ControlRevision == "" || current.Data.Status.Health != "unhealthy" {
		t.Fatalf("status: %+v %v", current, err)
	}
	invalidBody, _ := json.Marshal(api.ControlRequest{Revision: m.ControlRevision})
	response, raw := f.request(t, "POST", "/api/v2/monitors/"+m.ID+"/recover", managementOperatorToken, invalidBody, map[string]string{"If-Match": `"` + m.ControlRevision + `"`})
	if response.StatusCode != 422 {
		t.Fatalf("invalid reason: %d %s", response.StatusCode, raw)
	}
	_, err = f.sdk.Monitors.Recover(ctx, m.ID, api.ControlRequest{Revision: "stale", Reason: "Operator requested recovery"})
	varProblem(t, err, 412, "versionConflict")
	recovery, err := f.sdk.Monitors.Recover(ctx, m.ID, api.ControlRequest{Revision: m.ControlRevision, Reason: "Operator requested recovery"})
	if err != nil {
		t.Fatal(err)
	}
	if recovery.OperationID == "" || recovery.Data.State != "committed" || (recovery.Data.Applied == nil || *recovery.Data.Applied != 0) {
		t.Fatalf("intent not pending: %+v", recovery)
	}
	latest, ok := f.server.cfg.Store.Get(m.ID)
	if !ok {
		t.Fatal("monitor lost")
	}
	var action persistence.Action
	for _, a := range latest.Actions {
		if a.Manual {
			action = a
		}
	}
	if action.ID == "" || action.State != persistence.Queued || action.OperationID != recovery.OperationID {
		t.Fatal("manual intent was not queued", latest.Actions)
	}
	_, err = f.sdk.Monitors.Recover(ctx, m.ID, api.ControlRequest{Revision: m.ControlRevision, Reason: "Repeated request"})
	varProblem(t, err, 409, "recoveryIneligible")
	if err := f.catalog.CompleteOperation(ctx, recovery.OperationID, true); err != nil {
		t.Fatal(err)
	}
	receipt, err := f.sdk.Operations.Get(ctx, recovery.OperationID)
	if err != nil || (receipt.Data.Applied == nil || *receipt.Data.Applied != 1) {
		t.Fatal("owner completion missing", err)
	}
	history, err := f.sdk.History(ctx, cpra.ListOptions{MonitorID: m.ID})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range history.Data.Items {
		if e.ActionID == action.ID && e.Actor == "operator" && e.Reason == "Operator requested recovery" {
			found = true
		}
	}
	if !found {
		t.Fatal("manual recovery audit missing")
	}
}

func TestManagementActionShowsSeparateLateProviderEvidence(t *testing.T) {
	f := newManagementFixture(t, true)
	m, a, _ := unknownControlAction(t, f, "late", false)
	ctx := context.Background()
	before, err := f.sdk.Actions.Get(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	response, wire := f.request(t, "GET", "/api/v2/actions/"+a.ID, managementOperatorToken, nil, nil)
	if response.StatusCode != 200 || !strings.Contains(string(wire), `"executorFenced":false`) {
		t.Fatal("known unfenced state omitted", string(wire))
	}
	at := time.Now().UTC()
	results, err := f.server.cfg.Store.Submit(ctx, []persistence.Command{{Kind: "late_result", MonitorID: m.ID, Revision: a.Revision, ActionID: a.ID, At: at, ExecutionStart: a.StartedAt, ExecutionEnd: at, Outcome: "success"}})
	if err != nil || len(results) != 1 || results[0].Err != nil {
		t.Fatalf("late result: %+v %v", results, err)
	}
	after, err := f.sdk.Actions.Get(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Data.State != "unknown" || !after.Data.Held || after.Data.LateEvidence == nil || after.Data.LateEvidence.Outcome != "accepted" || after.Data.ReviewRevision == before.Data.ReviewRevision || after.Data.UpdatedAt == nil || !after.Data.UpdatedAt.Equal(at) {
		t.Fatalf("late observation missing: %+v", after)
	}
	if after.Data.CreatedAt != nil {
		t.Fatal("unknown creation time fabricated")
	}
	raw, _ := json.Marshal(after.Data)
	if strings.Contains(string(raw), "0001-01-01") {
		t.Fatal("unavailable timestamp serialized as year 0001")
	}
}
