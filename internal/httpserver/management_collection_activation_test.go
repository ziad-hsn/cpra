package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/httpauth"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func activationHTTPValidated(t *testing.T, f *managementFixture, resources ...api.Resource) api.Operation {
	t.Helper()
	op := validationHTTPOperation(t, f, resources...)
	if _, err := f.sdk.Operations.Validate(t.Context(), op.ID); err != nil {
		t.Fatal(err)
	}
	validationHTTPWorker(t, f)
	validationHTTPResult(t, f, "/api/v2/operations/"+op.ID+"/validation")
	return op
}

func TestCollectionActivationHTTPOriginalAdmissionAndRetry(t *testing.T) {
	f, store, _, wrapper := newPreflightRaftFixture(t)
	validationHTTPAuthority(t, f)
	var requests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer target.Close()
	secret := "PRIVATE-ACTIVATION-HTTP-CANARY"
	op := activationHTTPValidated(t, f, preflightMonitor("service", target.URL), managementResource("Credential", "private", api.CredentialSpec{Value: &secret}))
	before, found, err := store.CollectionGet(op.ID)
	if err != nil || !found {
		t.Fatal(err)
	}
	unwraps := wrapper.unwraps.Load()
	path := "/api/v2/operations/" + op.ID
	response, body := f.request(t, http.MethodPost, path+"/activate", managementOperatorToken, nil, nil)
	var admitted api.Operation
	if response.StatusCode != http.StatusOK || response.TLS == nil || api.DecodeResponse(body, &admitted) != nil || admitted.ID != op.ID || admitted.State != "applying" ||
		response.Header.Get("X-Operation-ID") != op.ID || response.Header.Get("Location") != path || response.Header.Get("Retry-After") != "5" ||
		admitted.ContentDigest != op.ContentDigest || admitted.IdentityFormat != op.IdentityFormat || !reflect.DeepEqual(admitted.ItemCount, op.ItemCount) {
		t.Fatal("public activation lost the original admission contract", response.StatusCode)
	}
	head, found, err := store.CollectionGet(op.ID)
	if err != nil || !found || head.Activation == nil || head.Activation.PlanID != before.Plan.Header.PlanID || head.Activation.ResultID != before.Validation.Header.ResultID ||
		head.Execution != nil || head.ProgressDigest != before.ProgressDigest || !reflect.DeepEqual(head.Plan, before.Plan) || !reflect.DeepEqual(head.Validation, before.Validation) {
		t.Fatal("activation changed original artifacts or ran execution in the HTTP handler", err)
	}
	index := store.Status().CommittedIndex
	for range 2 {
		retry, err := f.sdk.Operations.Activate(t.Context(), op.ID)
		if err != nil || retry == nil || retry.OperationID != op.ID || retry.Data.State != "applying" || !reflect.DeepEqual(retry.Data, admitted) {
			t.Fatal("activation retry did not reconcile original operation", err)
		}
	}
	current, _, err := store.CollectionGet(op.ID)
	if err != nil || !reflect.DeepEqual(current.Activation, head.Activation) || store.Status().CommittedIndex != index {
		t.Fatal("activation retry wrote a replacement admission", err)
	}
	if wrapper.unwraps.Load() != unwraps || requests.Load() != 0 || bytes.Contains(body, []byte(secret)) {
		t.Fatal("activation decrypted inputs, invoked a provider or exposed private data")
	}
}

func TestCollectionActivationHTTPAuthorizationAndMessageBoundary(t *testing.T) {
	f := newManagementFixture(t, true)
	validationHTTPAuthority(t, f)
	op := activationHTTPValidated(t, f, preflightMonitor("service", "https://unused.example.test"))
	path := "/api/v2/operations/" + op.ID + "/activate"
	before := f.server.cfg.Store.Status().CommittedIndex
	for _, test := range []struct {
		name, token, suffix string
		body                []byte
		headers             map[string]string
		status              int
	}{
		{name: "anonymous", status: 401},
		{name: "reader", token: managementReaderToken, status: 403},
		{name: "legacy", token: managementLegacyToken, status: 403},
		{name: "query", token: managementOperatorToken, suffix: "?force=true", status: 400},
		{name: "body", token: managementOperatorToken, body: []byte("{}"), status: 403},
		{name: "whitespace", token: managementOperatorToken, body: []byte(" "), status: 403},
		{name: "encoding", token: managementOperatorToken, headers: map[string]string{"Content-Encoding": "gzip"}, status: 415},
		{name: "origin", token: managementOperatorToken, headers: map[string]string{"Origin": "https://another.example.test"}, status: 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, _ := f.request(t, http.MethodPost, path+test.suffix, test.token, test.body, test.headers)
			if response.StatusCode != test.status {
				t.Fatal("activation accepted invalid credentials or message", response.StatusCode, test.status)
			}
		})
	}
	if f.server.cfg.Store.Status().CommittedIndex != before {
		t.Fatal("rejected activation requests changed durable state")
	}
	if err := f.server.StopAdmission(t.Context()); err != nil {
		t.Fatal(err)
	}
	response, _ := f.request(t, http.MethodPost, path, managementOperatorToken, nil, nil)
	if response.StatusCode != http.StatusServiceUnavailable || f.server.cfg.Store.Status().CommittedIndex != before {
		t.Fatal("stopped admission permitted collection activation", response.StatusCode)
	}
}

func TestCollectionActivationHTTPUnvalidatedAndForeignOwner(t *testing.T) {
	f := newManagementFixture(t, true)
	otherToken := "other-operator-012345678901234567890123456789"
	verifier, err := httpauth.HashToken(otherToken)
	if err != nil {
		t.Fatal(err)
	}
	f.authConfig.Principals = append(f.authConfig.Principals, httpauth.Principal{ID: "another", Role: httpauth.Operator, TokenSHA256: verifier})
	if err := f.server.cfg.ManagementAuth.Replace(f.authConfig); err != nil {
		t.Fatal(err)
	}
	validationHTTPAuthority(t, f)
	op := validationHTTPOperation(t, f, preflightMonitor("service", "https://unused.example.test"))
	path := "/api/v2/operations/" + op.ID + "/activate"
	before := f.server.cfg.Store.Status().CommittedIndex
	for _, test := range []struct {
		token  string
		status int
	}{{managementOperatorToken, 409}, {otherToken, 404}} {
		response, body := f.request(t, http.MethodPost, path, test.token, nil, nil)
		var problem api.Problem
		if response.StatusCode != test.status || json.Unmarshal(body, &problem) != nil {
			t.Fatal("unvalidated or foreign collection admitted", response.StatusCode)
		}
	}
	head, found, err := f.server.cfg.Store.CollectionGet(op.ID)
	if err != nil || !found || head.Activation != nil || f.server.cfg.Store.Status().CommittedIndex != before {
		t.Fatal("rejected requests wrote an activation", err)
	}
}

func TestCollectionActivationHTTPReconcilesCanceledOriginal(t *testing.T) {
	f := newManagementFixture(t, true)
	validationHTTPAuthority(t, f)
	op := activationHTTPValidated(t, f, preflightMonitor("service", "https://unused.example.test"))
	first, err := f.sdk.Operations.Activate(t.Context(), op.ID)
	if err != nil || first == nil {
		t.Fatal(err)
	}
	if _, err := f.sdk.Operations.Cancel(t.Context(), op.ID); err != nil {
		t.Fatal(err)
	}
	before, _, err := f.server.cfg.Store.CollectionGet(op.ID)
	if err != nil {
		t.Fatal(err)
	}
	index := f.server.cfg.Store.Status().CommittedIndex
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	read, err := f.sdk.Operations.Activate(ctx, op.ID)
	if err != nil || read == nil || read.Data.ID != op.ID || read.Data.State != "canceled" {
		t.Fatal("repeat activation lost original cancellation", err)
	}
	after, _, err := f.server.cfg.Store.CollectionGet(op.ID)
	if err != nil || !reflect.DeepEqual(before, after) || f.server.cfg.Store.Status().CommittedIndex != index {
		t.Fatal("repeat activation restarted canceled work", err)
	}
	if _, err := f.catalog.Get(t.Context(), "Monitor", "service"); err == nil {
		t.Fatal("HTTP admission executed a monitor")
	}
}

func TestCollectionActivationHTTPFinalResponseFence(t *testing.T) {
	for _, fault := range []string{"pending-storage", "ready-storage", "ready-deadline", "ready-cutoff", "revoked"} {
		t.Run(fault, func(t *testing.T) {
			var f *managementFixture
			var head persistence.CollectionState
			if fault == "pending-storage" {
				f = newManagementFixture(t, true)
				head = executionHTTPAdmit(t, f, preflightMonitor("service", "https://unused.example.test"))
			} else {
				f, head = executionHTTPFixture(t, 1)
			}
			m := f.server.managementHTTP
			at := time.Now().UTC()
			observation, err := f.catalog.CollectionOperationObservation(t.Context(), head.ID, head.Actor, at)
			if err != nil || observation.Operation.ExecutionResult == nil {
				t.Fatal("capture protected response", err)
			}
			request := httptest.NewRequest(http.MethodPost, "https://example.test/api/v2/operations/"+head.ID+"/activate", nil)
			request.Header.Set("Authorization", "Bearer "+managementOperatorToken)
			response := httptest.NewRecorder()
			managementHeaders(response)
			response.Header().Set("X-Operation-ID", head.ID)
			if fault == "revoked" {
				policy := f.authConfig
				policy.Principals = append([]httpauth.Principal(nil), policy.Principals...)
				for i := range policy.Principals {
					if policy.Principals[i].ID == head.Actor {
						policy.Principals[i].Revoked = true
					}
				}
				if err := f.server.cfg.ManagementAuth.Replace(policy); err != nil {
					t.Fatal(err)
				}
				m.publishCollectionActivation(response, request, head.Actor, observation)
			} else {
				// The protected read has finished. Stop inside final policy admission
				// before the metadata check, then change storage or retention.
				entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				t.Cleanup(unblock)
				m.now = func() time.Time {
					close(entered)
					<-release
					if fault == "ready-deadline" {
						return head.ExecutionResult.Summary.FinalizedAt.AddDate(0, 0, 30)
					}
					return at
				}
				go func() {
					defer close(done)
					m.publishCollectionActivation(response, request, head.Actor, observation)
				}()
				select {
				case <-entered:
				case <-time.After(2 * time.Second):
					t.Fatal("final policy admission was not reached")
				}
				if fault == "pending-storage" || fault == "ready-storage" {
					err = f.server.cfg.Store.Close()
				} else if fault == "ready-cutoff" {
					err = f.server.cfg.Store.History().Expire(head.ExecutionResult.Summary.FinalizedAt.AddDate(0, 0, 30))
				}
				unblock()
				if err != nil {
					t.Fatal(err)
				}
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Fatal("final response did not stop")
				}
			}
			want := http.StatusServiceUnavailable
			if fault == "ready-deadline" || fault == "ready-cutoff" {
				want = http.StatusGone
			} else if fault == "revoked" {
				want = http.StatusUnauthorized
			}
			var problem api.Problem
			if response.Code != want || json.Unmarshal(response.Body.Bytes(), &problem) != nil || problem.Status != int64(want) ||
				response.Header().Get("X-Operation-ID") != head.ID || response.Header().Get("Location") != "" || bytes.Contains(response.Body.Bytes(), []byte("executionResult")) {
				t.Fatal("final admission disclosed a stale operation response", response.Code, response.Body.String())
			}
		})
	}
}
