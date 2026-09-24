package cprafixture

import (
	"bytes"
	"context"

	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

func monitorResource(t *testing.T, id string) api.Resource {
	t.Helper()
	driver, err := api.Driver("check", "http", api.PulseHTTPConfig{URL: api.Pointer("https://service.example.test/health")})
	if err != nil {
		t.Fatal(err)
	}
	spec, err := json.Marshal(api.MonitorSpec{Check: api.CheckSpec{Driver: driver, Interval: "60s", Timeout: "5s"}})
	if err != nil {
		t.Fatal(err)
	}
	return api.Resource{APIVersion: api.APIVersion, Kind: "Monitor", Metadata: api.Metadata{ID: id}, Spec: spec}
}

func fixtureRequest(h *Handler, method, path, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("If-None-Match", "*")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestAuthenticationAndStrictBoundedInput(t *testing.T) {
	h := NewHandler()
	raw, err := json.Marshal(monitorResource(t, "first"))
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"", "private-secret"} {
		w := fixtureRequest(h, http.MethodPost, "/api/v2/monitors", token, string(raw))
		if w.Code != 401 || strings.Contains(w.Body.String(), "private-secret") {
			t.Fatalf("auth status=%d body=%s", w.Code, w.Body.String())
		}
	}
	for name, body := range map[string]string{
		"trailing":  string(raw) + ` {}`,
		"duplicate": strings.Replace(string(raw), `"id":"first"`, `"id":"second","id":"first"`, 1),
		"overflow":  `{"metadata":"` + strings.Repeat("x", api.MaxResourceBytes) + `"}`,
		"unknown":   strings.TrimSuffix(string(raw), "}") + `,"unexpected":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			h := NewHandler()
			w := fixtureRequest(h, http.MethodPost, "/api/v2/monitors", Token, body)
			if w.Code < 400 || len(h.Snapshot()) != 0 {
				t.Fatalf("malformed data accepted status=%d resources=%d", w.Code, len(h.Snapshot()))
			}
		})
	}
}

func sdkClient(t *testing.T, s *Server) *cpra.Client {
	t.Helper()
	c, err := cpra.New(cpra.Config{BaseURL: s.URL, AuthToken: Token, AllowInsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.CloseIdleConnections)
	return c
}

func createCollection(t *testing.T, c *cpra.Client, ids ...string) (*cpra.Response[api.Operation], []api.ApplyItem) {
	t.Helper()
	key := []byte(strings.Repeat("k", 32))
	defer clear(key)
	fingerprint := [32]byte{1}
	source, _ := commitment.SourceToken(1)
	accumulator, err := commitment.NewAccumulator(key, uint64(len(ids)), fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	defer accumulator.Close()
	items := make([]api.ApplyItem, 0, len(ids))
	for index, id := range ids {
		resource := monitorResource(t, id)
		raw, err := json.Marshal(resource)
		if err != nil {
			t.Fatal(err)
		}
		position := commitment.Position{ID: resource.Kind + "/" + id, Ordinal: uint64(index + 1), Source: commitment.SourcePosition{Token: source, Document: 1, Item: uint64(index + 1)}}
		mac, err := commitment.ItemMAC(key, position, raw)
		if err != nil {
			t.Fatal(err)
		}
		if err = accumulator.Add(position, mac); err != nil {
			t.Fatal(err)
		}
		items = append(items, api.ApplyItem{ID: position.ID, Ordinal: int64(position.Ordinal), Source: source, SourceDocument: 1, SourceItem: int64(index + 1), Resource: resource, ContentDigest: hex.EncodeToString(mac[:])})
	}
	digest, err := accumulator.Finish()
	if err != nil {
		t.Fatal(err)
	}
	identity := api.CollectionPrepareRequest{IdentityFormat: commitment.Format, IdentityKey: api.Pointer(hex.EncodeToString(key)), SourceFingerprint: api.Pointer(hex.EncodeToString(fingerprint[:])), ContentDigest: hex.EncodeToString(digest[:]), ItemCount: int64(len(ids))}
	admission, err := c.Operations.Prepare(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := c.Operations.Create(context.Background(), api.OperationCreateRequest{AdmissionTicket: admission.Data.Ticket, IdentityFormat: commitment.Format, IdentityKey: identity.IdentityKey, SourceFingerprint: identity.SourceFingerprint, ContentDigest: identity.ContentDigest, ItemCount: identity.ItemCount})
	if err != nil {
		t.Fatal(err)
	}
	return operation, items
}

func TestValidationSealsOriginalInputAndReconcilesReads(t *testing.T) {
	s := New()
	defer s.Close()
	c := sdkClient(t, s)
	ctx := context.Background()
	op, values := createCollection(t, c, "first")
	var err error
	items := api.UploadRequest{Items: values}
	if _, err = c.Operations.Upload(ctx, op.Data.ID, items); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Operations.Validate(ctx, op.Data.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Operations.Upload(ctx, op.Data.ID, items); err == nil {
		t.Fatal("sealed validation accepted further upload")
	}
	original, err := c.Operations.WaitValidation(ctx, op.Data.ID)
	if err != nil || !original.Data.Summary.Valid {
		t.Fatal("sealed result unavailable", err)
	}
	if len(s.Snapshot()) != 0 {
		t.Fatal("stale preflight activated resources")
	}
	if _, err = c.Operations.Validate(ctx, op.Data.ID); err != nil {
		t.Fatal(err)
	}
	reconciled, err := c.Operations.Validation(ctx, op.Data.ID, cpra.ValidationPageOptions{})
	if err != nil || reconciled.Data.Summary.ResultID != original.Data.Summary.ResultID {
		t.Fatal("validation retry replaced result", err)
	}
	first, err := c.Operations.Activate(ctx, op.Data.ID)
	if err != nil || first.Data.Applied == nil || *first.Data.Applied != 1 {
		t.Fatalf("activate=%+v err=%v", first, err)
	}
	before := s.Snapshot()[0].Metadata.ResourceVersion
	if _, err = c.Operations.Activate(ctx, op.Data.ID); err != nil {
		t.Fatal(err)
	}
	if s.Snapshot()[0].Metadata.ResourceVersion != before {
		t.Fatal("activation replay applied twice")
	}
}

func TestChunkDuplicateIDsCannotSilentlyReplaceAnItem(t *testing.T) {
	s := New()
	defer s.Close()
	c := sdkClient(t, s)
	ctx := context.Background()
	op, values := createCollection(t, c, "first", "second")
	var err error
	first, second := values[0], values[1]
	second.ID = first.ID
	if _, err = c.Operations.Upload(ctx, op.Data.ID, api.UploadRequest{Items: []api.ApplyItem{first, second}}); err == nil {
		t.Fatal("duplicate item ID accepted")
	}
	got, err := c.Operations.Get(ctx, op.Data.ID)
	if err != nil || got.Data.Uploaded == nil || *got.Data.Uploaded != 0 || len(s.Snapshot()) != 0 {
		t.Fatalf("invalid chunk partially stored: %+v %v", got, err)
	}
}

func TestFrozenCollectionAppliesThroughFixtureAdmission(t *testing.T) {
	s := New()
	defer s.Close()
	c := sdkClient(t, s)
	frozen, err := collection.FreezeResources(context.Background(), collection.Slice([]api.Resource{monitorResource(t, "first"), monitorResource(t, "second")}), collection.Options{MaxResources: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer frozen.Close()
	result, err := collection.Apply(context.Background(), c.Operations, frozen)
	if err != nil || result.OperationID == "" || result.Operation.State != "completed" || result.Operation.Applied == nil || *result.Operation.Applied != 2 {
		t.Fatal("frozen collection failed against HTTP fixture", err)
	}
	if len(s.Snapshot()) != 2 {
		t.Fatal("fixture did not apply both resources")
	}
	result, err = collection.Wait(context.Background(), c.Operations, result)
	if err != nil || result.Operation.ExecutionResult == nil || result.Operation.ExecutionResult.Summary.ChildApplied != 2 || len(result.Operation.Items) != 2 {
		t.Fatal("fixture omitted original retained execution result", err)
	}
	s.handler.mu.Lock()
	defer s.handler.mu.Unlock()
	if len(s.handler.admissions) != 1 || len(s.handler.operations) != 1 {
		t.Fatal("unexpected preparation/operation allocation")
	}
}

func TestFixtureOriginalTicketReconciliationAndCommitment(t *testing.T) {
	s := New()
	defer s.Close()
	c := sdkClient(t, s)
	operation, items := createCollection(t, c, "first")
	s.handler.mu.Lock()
	var ticket string
	for value := range s.handler.admissions {
		ticket = value
	}
	identity := s.handler.admissions[ticket].identity
	s.handler.mu.Unlock()
	request := api.OperationCreateRequest{AdmissionTicket: ticket, IdentityFormat: commitment.Format, IdentityKey: identity.IdentityKey, SourceFingerprint: identity.SourceFingerprint, ContentDigest: identity.ContentDigest, ItemCount: identity.ItemCount}
	for range 2 {
		reconciled, err := c.Operations.Create(context.Background(), request)
		if err != nil || reconciled.Data.ID != operation.Data.ID {
			t.Fatal("fixture did not reconcile original ticket", err)
		}
	}
	changed := request
	changed.ContentDigest = strings.Repeat("a", 64)
	if _, err := c.Operations.Create(context.Background(), changed); !errors.Is(err, cpra.ErrConflict) {
		t.Fatal("changed ticket identity accepted", err)
	}
	items[0].Resource = monitorResource(t, "changed")
	items[0].ID = "Monitor/changed"
	if _, err := c.Operations.Upload(context.Background(), operation.Data.ID, api.UploadRequest{Items: items}); !errors.Is(err, cpra.ErrInvalid) {
		t.Fatal("changed resource accepted without matching commitment", err)
	}
	s.handler.mu.Lock()
	s.handler.admissions[ticket].expires = time.Unix(1, 0)
	count := len(s.handler.operations)
	s.handler.mu.Unlock()
	if _, err := c.Operations.Create(context.Background(), request); !errors.Is(err, cpra.ErrExpired) {
		t.Fatal("expired ticket created operation", err)
	}
	if count != 1 || len(s.Snapshot()) != 0 {
		t.Fatal("fixture admitted duplicate or active change")
	}
}

func TestConditionalPatchAndSnapshotIsolation(t *testing.T) {
	s := New()
	defer s.Close()
	c := sdkClient(t, s)
	ctx := context.Background()
	r := monitorResource(t, "first")
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var monitor api.Monitor
	if err := json.Unmarshal(b, &monitor); err != nil {
		t.Fatal(err)
	}
	created, err := c.Monitors.Create(ctx, monitor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Monitors.Patch(ctx, "first", "stale-version", api.MergePatch(`{"spec":{"enabled":false}}`)); !errors.Is(err, cpra.ErrConflict) {
		t.Fatalf("stale patch err=%v", err)
	}
	patched, err := c.Monitors.Disable(ctx, "first", created.ResourceVersion)
	if err != nil {
		t.Fatal(err)
	}
	if patched.Data.Spec.Enabled == nil || *patched.Data.Spec.Enabled {
		t.Fatal("false patch value lost")
	}
	copy := s.Snapshot()
	copy[0].Metadata.ID = "mutated-copy"
	copy[0].Spec[0] = '['
	if s.Snapshot()[0].Metadata.ID != "first" || !json.Valid(s.Snapshot()[0].Spec) {
		t.Fatal("Snapshot leaked mutable server state")
	}
}

func TestDecodeClosesRequestBodyOnError(t *testing.T) {
	body := &closingBody{Reader: bytes.NewBufferString("not-json")}
	r := httptest.NewRequest(http.MethodPost, "/", body)
	w := httptest.NewRecorder()
	var out api.Resource
	if decode(w, r, &out, 32) || !body.closed {
		t.Fatal("decode accepted invalid JSON or did not close body")
	}
}

type closingBody struct {
	io.Reader
	closed bool
}

func (b *closingBody) Close() error { b.closed = true; return nil }
