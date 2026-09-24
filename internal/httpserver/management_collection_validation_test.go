package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func validationHTTPAuthority(t *testing.T, f *managementFixture) {
	t.Helper()
	command := persistence.AuthenticationCommand{Mode: "bootstrap", Epoch: uuid.NewString(), Revision: uuid.NewString(), Actor: "local/fixture", At: time.Now().UTC(), LegacyTokenSHA256: f.authConfig.LegacyTokenSHA256}
	for _, principal := range f.authConfig.Principals {
		command.Principals = append(command.Principals, persistence.AuthenticationPrincipal{ID: principal.ID, Role: principal.Role, TokenSHA256: principal.TokenSHA256, ExpiresAt: principal.ExpiresAt})
	}
	state, err := f.server.cfg.Store.CommitAuthentication(t.Context(), command)
	if err != nil {
		t.Fatal(err)
	}
	if !state.BootstrapConsumed {
		t.Fatal("fixture did not install committed authentication")
	}
}

func validationHTTPOperation(t *testing.T, f *managementFixture, resources ...api.Resource) api.Operation {
	t.Helper()
	frozen := preflightInventory(t, resources...)
	identity := collectionHTTPIdentity(frozen)
	ticket, err := f.sdk.Operations.Prepare(t.Context(), identity)
	if err != nil {
		t.Fatal("prepare", err)
	}
	created, err := f.sdk.Operations.Create(t.Context(), api.OperationCreateRequest{AdmissionTicket: ticket.Data.Ticket,
		IdentityFormat: api.OperationCreateRequestIdentityFormat(identity.IdentityFormat), IdentityKey: identity.IdentityKey, SourceFingerprint: identity.SourceFingerprint,
		ContentDigest: identity.ContentDigest, ItemCount: identity.ItemCount})
	if err != nil {
		t.Fatal("create", err)
	}
	uploaded, err := f.sdk.Operations.Upload(t.Context(), created.Data.ID, api.UploadRequest{Items: frozen.Items})
	if err != nil {
		t.Fatal("upload", err)
	}
	return uploaded.Data
}

func validationHTTPWorker(t *testing.T, f *managementFixture) *management.CollectionValidationCoordinator {
	t.Helper()
	worker, err := f.catalog.StartCollectionValidationCoordinator(t.Context(), time.Now)
	if err != nil {
		t.Fatal("start coordinator", err)
	}
	t.Cleanup(func() {
		worker.BeginStop()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := worker.Wait(ctx); err != nil {
			t.Error("join coordinator", err)
		}
	})
	return worker
}

func validationHTTPResult(t *testing.T, f *managementFixture, path string) api.ValidationResultPage {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		response, body := f.request(t, http.MethodGet, path, managementOperatorToken, nil, nil)
		if response.StatusCode == http.StatusOK {
			var result api.ValidationResultPage
			if response.TLS == nil || api.DecodeResponse(body, &result) != nil {
				t.Fatal("invalid TLS result page")
			}
			return result
		}
		var problem api.Problem
		if response.StatusCode != http.StatusConflict || json.Unmarshal(body, &problem) != nil || problem.Code != "validationPending" || time.Now().After(deadline) {
			t.Fatalf("validation did not complete: HTTP %d code %s", response.StatusCode, problem.Code)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCollectionValidationHTTPOriginalAsyncResultAndPagination(t *testing.T) {
	f, store, _, wrapper := newPreflightRaftFixture(t)
	validationHTTPAuthority(t, f)
	var providerCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { providerCalls.Add(1) }))
	defer target.Close()
	secret := "PRIVATE-VALIDATION-HTTP-CANARY"
	original := validationHTTPOperation(t, f, preflightMonitor("first", target.URL), managementResource("Credential", "private", api.CredentialSpec{Value: &secret}))
	path := "/api/v2/operations/" + original.ID
	if original.Validated != nil {
		t.Fatal("upload invented a false validation verdict")
	}
	before := store.Status().CommittedIndex
	for range 2 {
		response, body := f.request(t, http.MethodPost, path+"/validate", managementOperatorToken, nil, nil)
		var pending api.Operation
		if response.StatusCode != 202 || response.TLS == nil || json.Unmarshal(body, &pending) != nil || pending.ID != original.ID || pending.State != "validating" || pending.Validated != nil ||
			response.Header.Get("X-Operation-ID") != original.ID || response.Header.Get("Location") != path+"/validation" || response.Header.Get("Retry-After") != "5" {
			t.Fatal("asynchronous admission did not preserve original operation", response.StatusCode)
		}
	}
	if store.Status().CommittedIndex != before+1 {
		t.Fatal("repeated validation appended another request")
	}
	response, body := f.request(t, http.MethodGet, path+"/validation", managementOperatorToken, nil, nil)
	var problem api.Problem
	if response.StatusCode != 409 || json.Unmarshal(body, &problem) != nil || problem.Code != "validationPending" || response.Header.Get("Retry-After") != "5" || response.Header.Get("X-Operation-ID") != original.ID {
		t.Fatal("pending request was disguised as a verdict", response.StatusCode)
	}
	validationHTTPWorker(t, f)
	first := validationHTTPResult(t, f, path+"/validation?limit=1")
	if !first.Summary.Valid || first.Summary.Count != 2 || first.Summary.ResultID == "" || first.Summary.PlanID == "" || len(first.Items) != 1 || first.Items[0].Ordinal != 1 || first.NextCursor == "" ||
		first.ContentDigest != original.ContentDigest || first.IdentityFormat != string(original.IdentityFormat) || first.OperationID != original.ID {
		t.Fatal("first result lost original identity or bounded order")
	}
	second := validationHTTPResult(t, f, path+"/validation?cursor="+url.QueryEscape(first.NextCursor))
	if len(second.Items) != 1 || second.Items[0].Ordinal != 2 || second.Items[0].Source != "source.00000000000000000002" || second.Items[0].SourceDocument != 1 || second.Items[0].SourceItem != 1 || second.NextCursor != "" || second.Summary != first.Summary {
		t.Fatal("continuation changed original result or source coordinates")
	}
	// Exercise generated SDK query serialization against the strict production
	// handler. A missing cursor must be omitted, never encoded as cursor=.
	sdkFirst, err := f.sdk.Operations.Validation(t.Context(), original.ID, cpra.ValidationPageOptions{Limit: 1})
	if err != nil || sdkFirst == nil || len(sdkFirst.Data.Items) != 1 || sdkFirst.Data.Summary != first.Summary || sdkFirst.Data.NextCursor == "" {
		t.Fatal("SDK first page did not preserve original result", err)
	}
	sdkNext, err := f.sdk.Operations.Validation(t.Context(), original.ID, cpra.ValidationPageOptions{Limit: 1, Cursor: sdkFirst.Data.NextCursor})
	if err != nil || sdkNext == nil || len(sdkNext.Data.Items) != 1 || sdkNext.Data.Items[0].Ordinal != 2 || sdkNext.Data.Summary != first.Summary || sdkNext.Data.NextCursor != "" {
		t.Fatal("SDK cursor did not continue original result", err)
	}
	waited, err := f.sdk.Operations.WaitValidation(t.Context(), original.ID)
	if err != nil || waited == nil || len(waited.Data.Items) != 2 || waited.Data.Summary != first.Summary {
		t.Fatal("SDK wait did not read original completed result", err)
	}
	opens := wrapper.unwraps.Load()
	for range 2 {
		response, body = f.request(t, http.MethodPost, path+"/validate", managementOperatorToken, nil, nil)
		var repeated api.Operation
		if response.StatusCode != 202 || json.Unmarshal(body, &repeated) != nil || repeated.ID != original.ID || repeated.State != "validated" || repeated.Validated == nil || !*repeated.Validated {
			t.Fatal("repeated admission lost original completed verdict", response.StatusCode)
		}
	}
	view, err := store.CatalogSnapshot()
	if err != nil || view.Len() != 0 || providerCalls.Load() != 0 || wrapper.unwraps.Load() != opens {
		t.Fatal("validation activated input, invoked provider or recompiled original result", err)
	}
	for _, value := range []any{first, second} {
		raw, err := json.Marshal(value)
		if err != nil || bytes.Contains(raw, []byte(secret)) || bytes.Contains(raw, []byte(target.URL)) || bytes.Contains(raw, []byte("authority")) {
			t.Fatal("result disclosed protected input")
		}
	}
}

func TestCollectionValidationHTTPAuthorizationAndCursorBinding(t *testing.T) {
	f := newManagementFixture(t, true)
	validationHTTPAuthority(t, f)
	original := validationHTTPOperation(t, f, preflightMonitor("first", "https://first.example.test"), preflightMonitor("second", "https://second.example.test"))
	path := "/api/v2/operations/" + original.ID
	for _, row := range []struct {
		method, suffix, token string
		status                int
	}{
		{http.MethodPost, "/validate", "", 401}, {http.MethodPost, "/validate", managementReaderToken, 403},
		{http.MethodGet, "/validation", "", 401}, {http.MethodGet, "/validation", managementReaderToken, 404},
	} {
		response, _ := f.request(t, row.method, path+row.suffix, row.token, nil, nil)
		if response.StatusCode != row.status {
			t.Fatal("authorization boundary", response.StatusCode, row.status)
		}
	}
	for _, query := range []string{"?limit=0", "?limit=501", "?limit=1&limit=2", "?limit=", "?after=0", "?cursor="} {
		response, _ := f.request(t, http.MethodGet, path+"/validation"+query, managementOperatorToken, nil, nil)
		if response.StatusCode != 400 {
			t.Fatal("invalid query accepted", query, response.StatusCode)
		}
	}
	for _, body := range []string{" ", "{}", strings.Repeat("x", 1024)} {
		response, _ := f.request(t, http.MethodPost, path+"/validate", managementOperatorToken, []byte(body), nil)
		if response.StatusCode < 400 || response.StatusCode >= 500 {
			t.Fatal("request body accepted", response.StatusCode)
		}
	}
	for _, suffix := range []string{"/validate?reason=private", "/validation?token=private"} {
		method := http.MethodGet
		if strings.Contains(suffix, "/validate?") {
			method = http.MethodPost
		}
		response, _ := f.request(t, method, path+suffix, managementOperatorToken, nil, nil)
		if response.StatusCode != 400 {
			t.Fatal("undocumented query accepted", response.StatusCode)
		}
	}
	if _, err := f.sdk.Operations.Validate(t.Context(), original.ID); err != nil {
		t.Fatal(err)
	}
	validationHTTPWorker(t, f)
	first := validationHTTPResult(t, f, path+"/validation?limit=1")
	for _, row := range []struct {
		path, token string
		status      int
	}{
		{path + "/validation?cursor=" + url.QueryEscape(first.NextCursor) + "&limit=2", managementOperatorToken, 400},
		{path + "/validation?cursor=" + url.QueryEscape(first.NextCursor) + "x", managementOperatorToken, 400},
		{path + "/validation?cursor=" + url.QueryEscape(first.NextCursor), managementReaderToken, 410},
		{"/api/v2/operations/op." + uuid.NewString() + ".00000000000000000001/validation?cursor=" + url.QueryEscape(first.NextCursor), managementOperatorToken, 400},
	} {
		response, _ := f.request(t, http.MethodGet, row.path, row.token, nil, nil)
		if response.StatusCode != row.status {
			t.Fatal("cursor binding", response.StatusCode, row.status)
		}
	}
	// Same committed policy, a distinct HTTP authorization generation. An old
	// cursor must not migrate even when the textual principal is unchanged.
	if err := f.server.managementHTTP.auth.Replace(f.authConfig); err != nil {
		t.Fatal(err)
	}
	response, _ := f.request(t, http.MethodGet, path+"/validation?cursor="+url.QueryEscape(first.NextCursor), managementOperatorToken, nil, nil)
	if response.StatusCode != 410 {
		t.Fatal("old policy cursor accepted", response.StatusCode)
	}
}

func TestCollectionValidationHTTPResultOutlivesUploadAndFailsForMissingHistory(t *testing.T) {
	f, store, directory, wrapper := newPreflightRaftFixture(t)
	validationHTTPAuthority(t, f)
	original := validationHTTPOperation(t, f, preflightMonitor("first", "https://first.example.test"))
	path := "/api/v2/operations/" + original.ID + "/validation"
	if _, err := f.sdk.Operations.Validate(t.Context(), original.ID); err != nil {
		t.Fatal(err)
	}
	worker := validationHTTPWorker(t, f)
	first := validationHTTPResult(t, f, path)
	worker.BeginStop()
	if err := worker.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	head, exists, err := store.CollectionGet(original.ID)
	if err != nil || !exists {
		t.Fatal("original staged header absent", err)
	}
	at := head.ExpiresAt.Add(time.Hour)
	f.server.managementHTTP.now = func() time.Time { return at }
	later := validationHTTPResult(t, f, path)
	if later.Summary != first.Summary {
		t.Fatal("upload expiry changed retained verdict")
	}
	paths, err := filepath.Glob(filepath.Join(directory, "history", "*.db"))
	if err != nil || len(paths) == 0 {
		t.Fatal("real retained segment missing", err)
	}
	opens := wrapper.unwraps.Load()
	if err := os.Rename(paths[0], paths[0]+".unavailable"); err != nil {
		t.Fatal(err)
	}
	defer os.Rename(paths[0]+".unavailable", paths[0])
	response, body := f.request(t, http.MethodGet, path, managementOperatorToken, nil, nil)
	var problem api.Problem
	if response.StatusCode != 503 || json.Unmarshal(body, &problem) != nil || problem.Code != "historyUnavailable" || wrapper.unwraps.Load() != opens {
		t.Fatal("missing retained history fabricated expiry/rejection or recompiled", response.StatusCode, problem.Code)
	}
	at = first.Summary.ExpiresAt
	response, _ = f.request(t, http.MethodGet, path, managementOperatorToken, nil, nil)
	if response.StatusCode != 410 {
		t.Fatal("original result retention renewed", response.StatusCode)
	}
}

func TestCollectionValidationHTTPRejectionAndNoVerdictRemainDistinct(t *testing.T) {
	for _, disposition := range []string{"rejected", "canceled", "notRequested"} {
		t.Run(disposition, func(t *testing.T) {
			f := newManagementFixture(t, true)
			validationHTTPAuthority(t, f)
			original := validationHTTPOperation(t, f, managementResource("Credential", "missing-value", api.CredentialSpec{}))
			path := "/api/v2/operations/" + original.ID
			if disposition == "canceled" {
				if _, err := f.sdk.Operations.Cancel(t.Context(), original.ID); err != nil {
					t.Fatal(err)
				}
			} else if disposition == "rejected" {
				if _, err := f.sdk.Operations.Validate(t.Context(), original.ID); err != nil {
					t.Fatal(err)
				}
				validationHTTPWorker(t, f)
				result := validationHTTPResult(t, f, path+"/validation")
				if result.Summary.Valid || result.Summary.Issue == "" || result.Summary.PlanID != "" || len(result.Items) != 1 || result.Items[0].Issue == "" {
					t.Fatal("rejection lost original evidence")
				}
				return
			}
			response, body := f.request(t, http.MethodGet, path+"/validation", managementOperatorToken, nil, nil)
			var problem api.Problem
			want := "validationCanceled"
			if disposition == "notRequested" {
				want = "validationNotRequested"
			}
			if response.StatusCode != 409 || json.Unmarshal(body, &problem) != nil || problem.Code != want || bytes.Contains(body, []byte(`"valid":false`)) {
				t.Fatal("absence of verdict misreported", response.StatusCode, problem.Code)
			}
		})
	}
}
