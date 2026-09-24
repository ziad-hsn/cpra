package httpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/httpauth"
	"github.com/ziad-hsn/cpra/internal/persistence"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestManagementOperationCollectionsShareSnapshotAndPreserveVisibility(t *testing.T) {
	f, store, _, wrapper := newPreflightRaftFixture(t)
	ordinary := operationListCredential(t, f, "existing")
	one, _ := collectionCancellationHTTPSetup(t, f, true)
	two, _ := collectionCancellationHTTPSetup(t, f, false)
	page, err := f.sdk.Operations.List(t.Context(), cpra.ListOptions{Limit: 1})
	if err != nil || page == nil || len(page.Data.Items) != 1 || page.Data.NextCursor == "" {
		t.Fatal("cannot start mixed operation snapshot", err)
	}
	expected := map[string]string{ordinary.OperationID: "committed", one.ID: "uploading", two.ID: "uploading"}
	seen := make(map[string]bool)
	for _, item := range page.Data.Items {
		seen[item.ID] = true
	}
	// Change both ordinary and collection state after the first snapshot page.
	// The frozen list must keep its original observations and exclude later IDs.
	if err := f.catalog.CompleteOperation(t.Context(), ordinary.OperationID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sdk.Operations.Cancel(t.Context(), one.ID); err != nil {
		t.Fatal(err)
	}
	head, exists, err := store.CollectionGet(one.ID)
	if err != nil || !exists {
		t.Fatal("missing canceled collection", err)
	}
	results, err := store.Submit(t.Context(), []persistence.Command{{Kind: "collection", At: time.Now().UTC(), Collection: &persistence.CollectionCommand{
		Action: "cleanup", OperationID: one.ID, UploadID: head.UploadID, Cleanup: &persistence.CollectionCleanup{
			ActivityAt: head.ActivityAt, Uploaded: head.Uploaded, EncodedBytes: head.EncodedBytes}}}})
	if err != nil || len(results) != 1 || results[0].Err != nil {
		t.Fatal("cleanup failed", err)
	}
	late, _ := collectionCancellationHTTPSetup(t, f, false)
	opens := wrapper.unwraps.Load()
	for pages := 0; page.Data.NextCursor != ""; pages++ {
		if pages > 20 {
			t.Fatal("operation cursor failed to advance")
		}
		cursor := page.Data.NextCursor
		page, err = f.sdk.Operations.List(t.Context(), cpra.ListOptions{Limit: 1, Cursor: cursor})
		if err != nil {
			t.Fatal("mixed snapshot continuation", err)
		}
		repeated, repeatErr := f.sdk.Operations.List(t.Context(), cpra.ListOptions{Limit: 1, Cursor: cursor})
		if repeatErr != nil || !reflect.DeepEqual(repeated.Data, page.Data) {
			t.Fatal("same cursor changed its original page", repeatErr)
		}
		for _, item := range page.Data.Items {
			if seen[item.ID] || item.ID == late.ID || expected[item.ID] != item.State {
				t.Fatal("snapshot duplicated, rebased or added an operation", item.ID, item.State)
			}
			seen[item.ID] = true
			if item.ID == one.ID || item.ID == two.ID {
				if item.IdentityFormat != one.IdentityFormat || item.ItemCount == nil || *item.ItemCount != 2 || item.Validated != nil || !admissionCountIs(item.Committed, 0) || !admissionCountIs(item.Applied, 0) || len(item.Items) != 0 {
					t.Fatal("collection list fabricated validation or resource changes")
				}
			}
		}
	}
	if len(seen) != len(expected) {
		t.Fatal("snapshot omitted an original operation", len(seen))
	}
	current, err := f.sdk.Operations.Get(t.Context(), one.ID)
	if err != nil || current.Data.State != "canceled" || current.Data.Uploaded == nil || *current.Data.Uploaded != 1 {
		t.Fatal("current owned receipt missing after cleanup", err)
	}
	all, err := f.sdk.Operations.List(t.Context(), cpra.ListOptions{})
	if err != nil || len(all.Data.Items) != 4 || all.Data.NextCursor != "" {
		t.Fatal("fresh snapshot omitted retained collection receipt", err)
	}
	for _, item := range all.Data.Items {
		if item.ID == one.ID && item.State != "canceled" {
			t.Fatal("fresh snapshot did not use terminal original receipt")
		}
	}
	const foreignToken = "list-foreign-operator-012345678901234567890123456789"
	hash, err := httpauth.HashToken(foreignToken)
	if err != nil {
		t.Fatal(err)
	}
	policy := f.authConfig
	policy.Principals = append(append([]httpauth.Principal(nil), policy.Principals...), httpauth.Principal{ID: "other-operator", Role: httpauth.Operator, TokenSHA256: hash})
	if err := f.server.cfg.ManagementAuth.Replace(policy); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{managementReaderToken, foreignToken} {
		response, body := f.request(t, http.MethodGet, "/api/v2/operations", token, nil, nil)
		var list api.OperationList
		if response.StatusCode != 200 || api.DecodeResponse(body, &list) != nil || len(list.Items) != 1 || list.Items[0].ID != ordinary.OperationID {
			t.Fatal("collection ownership altered ordinary visibility or exposed another actor", response.StatusCode)
		}
		for _, id := range []string{one.ID, two.ID, late.ID} {
			response, body := f.request(t, http.MethodGet, "/api/v2/operations/"+id, token, nil, nil)
			if response.StatusCode != 404 || bytes.Contains(body, []byte(id)) {
				t.Fatal("foreign collection detail disclosed original identity", response.StatusCode)
			}
		}
		response, _ = f.request(t, http.MethodGet, "/api/v2/operations/"+ordinary.OperationID, token, nil, nil)
		if response.StatusCode != 200 {
			t.Fatal("shared ordinary detail became owner-only", response.StatusCode)
		}
	}
	if wrapper.unwraps.Load() != opens {
		t.Fatal("operation observations decrypted staged input")
	}
}

func TestManagementOperationCollectionsExposeOriginalVerdictAndExpiredStaging(t *testing.T) {
	f := newManagementFixture(t, true)
	validationHTTPAuthority(t, f)
	original := validationHTTPOperation(t, f, preflightMonitor("one", "https://unused.example.test"))
	if _, err := f.sdk.Operations.Validate(t.Context(), original.ID); err != nil {
		t.Fatal(err)
	}
	worker := validationHTTPWorker(t, f)
	result := validationHTTPResult(t, f, "/api/v2/operations/"+original.ID+"/validation")
	worker.BeginStop()
	if err := worker.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	list, err := f.sdk.Operations.List(t.Context(), cpra.ListOptions{})
	if err != nil || len(list.Data.Items) != 1 || list.Data.Items[0].Validated == nil || !*list.Data.Items[0].Validated || list.Data.Items[0].ContentDigest != original.ContentDigest {
		t.Fatal("list did not expose original sealed verdict", err)
	}
	head, _, err := f.server.cfg.Store.CollectionGet(original.ID)
	if err != nil {
		t.Fatal(err)
	}
	f.server.managementHTTP.now = func() time.Time { return head.ExpiresAt }
	response, raw := f.request(t, http.MethodGet, "/api/v2/operations/"+original.ID, managementOperatorToken, nil, nil)
	var expired api.Operation
	if response.StatusCode != 200 || json.Unmarshal(raw, &expired) != nil || expired.ID != original.ID || expired.State != "expired" || expired.Validated != nil {
		t.Fatal("elapsed staging was fabricated as active or committed terminal state", response.StatusCode)
	}
	list, err = f.sdk.Operations.List(t.Context(), cpra.ListOptions{})
	if err != nil || len(list.Data.Items) != 1 || list.Data.Items[0].State != "expired" || list.Data.Items[0].Validated != nil {
		t.Fatal("list/detail expired observation disagreed", err)
	}
	retained, err := f.sdk.Operations.Validation(t.Context(), original.ID, cpra.ValidationPageOptions{})
	if err != nil || retained.Data.Summary.ResultID != result.Summary.ResultID {
		t.Fatal("staging expiry hid retained original validation", err)
	}
	current, _, err := f.server.cfg.Store.CollectionGet(original.ID)
	if err != nil || current.Phase != "validated" || !current.TerminalAt.IsZero() {
		t.Fatal("observation committed an expiry event", err)
	}
}
