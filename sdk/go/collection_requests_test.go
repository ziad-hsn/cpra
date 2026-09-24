package cpra

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

func validCollectionFixture(t *testing.T) api.PreflightRequest {
	t.Helper()
	key := []byte(strings.Repeat("k", 32))
	fingerprint := [32]byte{1}
	token, _ := commitment.SourceToken(1)
	resource := api.Resource{APIVersion: api.APIVersion, Kind: "Credential", Metadata: api.Metadata{ID: "équipe@ops"}, Spec: json.RawMessage(`{"value":"fictional-input"}`)}
	position := commitment.Position{Ordinal: 1, ID: resource.Kind + "/" + resource.Metadata.ID, Source: commitment.SourcePosition{Token: token, Document: 1, Item: 1}}
	raw, err := json.Marshal(resource)
	if err != nil {
		t.Fatal(err)
	}
	mac, err := commitment.ItemMAC(key, position, raw)
	if err != nil {
		t.Fatal(err)
	}
	a, err := commitment.NewAccumulator(key, 1, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err = a.Add(position, mac); err != nil {
		t.Fatal(err)
	}
	digest, err := a.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return api.PreflightRequest{IdentityFormat: commitment.Format, IdentityKey: api.Pointer(hex.EncodeToString(key)), SourceFingerprint: api.Pointer(hex.EncodeToString(fingerprint[:])), ContentDigest: hex.EncodeToString(digest[:]), ItemCount: 1, Items: []api.ApplyItem{{ID: position.ID, Source: token, Ordinal: 1, SourceDocument: 1, SourceItem: 1, ContentDigest: hex.EncodeToString(mac[:]), Resource: resource}}}
}

func TestCollectionRequestsValidateBeforeHTTP(t *testing.T) {
	for _, change := range []struct {
		name   string
		mutate func(*api.PreflightRequest)
	}{
		{"missing-key", func(r *api.PreflightRequest) { r.IdentityKey = nil }},
		{"empty-key", func(r *api.PreflightRequest) { r.IdentityKey = api.Pointer("") }},
		{"uppercase-key", func(r *api.PreflightRequest) { r.IdentityKey = api.Pointer(strings.Repeat("A", 64)) }},
		{"missing-fingerprint", func(r *api.PreflightRequest) { r.SourceFingerprint = nil }},
		{"unknown-format", func(r *api.PreflightRequest) { r.IdentityFormat = "other" }},
		{"old-digest", func(r *api.PreflightRequest) { r.ContentDigest = strings.Repeat("a", 64) }},
		{"count-mismatch", func(r *api.PreflightRequest) { r.ItemCount = 2 }},
		{"negative-count", func(r *api.PreflightRequest) { r.ItemCount = -1 }},
		{"too-many", func(r *api.PreflightRequest) { r.Items = make([]api.ApplyItem, 10001); r.ItemCount = 10001 }},
		{"bad-ordinal", func(r *api.PreflightRequest) { r.Items[0].Ordinal = -1 }},
		{"missing-document", func(r *api.PreflightRequest) { r.Items[0].SourceDocument = 0 }},
		{"source-path", func(r *api.PreflightRequest) { r.Items[0].Source = "/private/source.json" }},
		{"wrong-identity", func(r *api.PreflightRequest) { r.Items[0].ID = "Credential/other" }},
		{"wrong-payload", func(r *api.PreflightRequest) { r.Items[0].Resource.Spec = json.RawMessage(`{"value":"changed-input"}`) }},
		{"short-mac", func(r *api.PreflightRequest) { r.Items[0].ContentDigest = "short" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			calls := 0
			client := fixture(t, func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) }, nil)
			req := validCollectionFixture(t)
			change.mutate(&req)
			_, err := client.Operations.Preflight(context.Background(), req)
			if err == nil || calls != 0 || strings.Contains(err.Error(), "fictional-input") || strings.Contains(err.Error(), "private/source") || strings.Contains(err.Error(), "changed-input") {
				t.Fatal("invalid inventory submitted or input exposed", err)
			}
		})
	}
}

func TestCollectionCreationAndUploadRejectUnboundInputs(t *testing.T) {
	calls := 0
	client := fixture(t, func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) }, nil)
	for _, count := range []int64{-1, 0, 10_000_001} {
		r := validCollectionFixture(t)
		_, err := client.Operations.Create(context.Background(), api.OperationCreateRequest{AdmissionTicket: "fixture-ticket", IdentityFormat: api.OperationCreateRequestIdentityFormat(r.IdentityFormat), IdentityKey: r.IdentityKey, SourceFingerprint: r.SourceFingerprint, ContentDigest: r.ContentDigest, ItemCount: count})
		if err == nil {
			t.Fatal("invalid creation count accepted")
		}
	}
	for _, change := range []func(*api.UploadRequest){
		func(r *api.UploadRequest) { r.Items = nil }, func(r *api.UploadRequest) { r.Items[0].Ordinal = 0 },
		func(r *api.UploadRequest) { r.Items[0].SourceItem = 0 }, func(r *api.UploadRequest) { r.Items[0].ContentDigest = strings.Repeat("A", 64) },
		func(r *api.UploadRequest) { r.Items = append(r.Items, r.Items[0]) },
		func(r *api.UploadRequest) {
			r.Items[0].Resource.Spec = json.RawMessage(`{"value":"` + strings.Repeat("a", 1<<20) + `"}`)
		},
	} {
		r := api.UploadRequest{Items: validCollectionFixture(t).Items}
		change(&r)
		if _, err := client.Operations.Upload(context.Background(), "original", r); err == nil {
			t.Fatal("invalid upload accepted")
		}
	}
	if calls != 0 {
		t.Fatal("invalid collection request reached transport")
	}
}

func TestEphemeralPreflightLostReplyIsNotAnAmbiguousMutation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, HTTPClient: server.Client(), AuthToken: "local-fixture-token", ReadAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	_, err = client.Operations.Preflight(context.Background(), validCollectionFixture(t))
	if err == nil || errors.Is(err, ErrAmbiguous) || calls.Load() != 1 {
		t.Fatal("ephemeral preflight was misclassified or retried", err)
	}
}

func TestCollectionPreparationAndTicketValidationBeforeHTTP(t *testing.T) {
	r := validCollectionFixture(t)
	prepared := api.CollectionPrepareRequest{IdentityFormat: api.CollectionPrepareRequestIdentityFormat(r.IdentityFormat), IdentityKey: r.IdentityKey, SourceFingerprint: r.SourceFingerprint, ContentDigest: r.ContentDigest, ItemCount: r.ItemCount}
	create := api.OperationCreateRequest{AdmissionTicket: "opaque-original-ticket", IdentityFormat: api.OperationCreateRequestIdentityFormat(r.IdentityFormat), IdentityKey: r.IdentityKey, SourceFingerprint: r.SourceFingerprint, ContentDigest: r.ContentDigest, ItemCount: r.ItemCount}
	calls := 0
	c := fixture(t, func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(500) }, nil)
	for _, mutate := range []func(*api.CollectionPrepareRequest){
		func(r *api.CollectionPrepareRequest) { r.NormalizationProfile = "cpra.file.future.v2" },
		func(r *api.CollectionPrepareRequest) { r.IdentityKey = nil },
		func(r *api.CollectionPrepareRequest) { r.SourceFingerprint = nil },
		func(r *api.CollectionPrepareRequest) { r.ContentDigest = "invalid" },
		func(r *api.CollectionPrepareRequest) { r.IdentityFormat = "unknown" },
		func(r *api.CollectionPrepareRequest) { r.ItemCount = 0 },
	} {
		v := prepared
		mutate(&v)
		if _, err := c.Operations.Prepare(context.Background(), v); err == nil {
			t.Fatal("invalid preparation reached HTTP")
		}
	}
	for _, ticket := range []string{"", strings.Repeat("x", (128<<10)+1)} {
		v := create
		v.AdmissionTicket = ticket
		if _, err := c.Operations.Create(context.Background(), v); err == nil {
			t.Fatal("invalid ticket reached HTTP")
		}
	}
	changed := create
	changed.NormalizationProfile = "cpra.file.future.v2"
	if _, err := c.Operations.Create(context.Background(), changed); err == nil {
		t.Fatal("unsupported normalization profile reached HTTP")
	}
	if calls != 0 {
		t.Fatal("invalid admission reached server")
	}
}

func TestCollectionPreparationRejectsInvalidReceipt(t *testing.T) {
	r := validCollectionFixture(t)
	request := api.CollectionPrepareRequest{IdentityFormat: api.CollectionPrepareRequestIdentityFormat(r.IdentityFormat), IdentityKey: r.IdentityKey, SourceFingerprint: r.SourceFingerprint, ContentDigest: r.ContentDigest, ItemCount: r.ItemCount}
	for _, receipt := range []api.CollectionAdmission{{}, {Ticket: "private-ticket"}, {ExpiresAt: time.Now()}, {Ticket: strings.Repeat("x", (128<<10)+1), ExpiresAt: time.Now()}} {
		c := fixture(t, func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(receipt) }, nil)
		if _, err := c.Operations.Prepare(context.Background(), request); err == nil || strings.Contains(err.Error(), "private-ticket") {
			t.Fatal("invalid or exposed admission receipt", err)
		}
	}
}

func TestCollectionAdmissionLostRepliesAreNeverAutomaticallyRetried(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	defer server.Close()
	c, err := New(Config{BaseURL: server.URL, HTTPClient: server.Client(), AuthToken: "fixture-token", ReadAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseIdleConnections()
	r := validCollectionFixture(t)
	_, err = c.Operations.Prepare(context.Background(), api.CollectionPrepareRequest{IdentityFormat: api.CollectionPrepareRequestIdentityFormat(r.IdentityFormat), IdentityKey: r.IdentityKey, SourceFingerprint: r.SourceFingerprint, ContentDigest: r.ContentDigest, ItemCount: r.ItemCount})
	if err == nil || errors.Is(err, ErrAmbiguous) || calls.Load() != 1 {
		t.Fatal("preparation was retried or classified as operation creation", err)
	}
	_, err = c.Operations.Create(context.Background(), api.OperationCreateRequest{AdmissionTicket: "original-ticket", IdentityFormat: api.OperationCreateRequestIdentityFormat(r.IdentityFormat), IdentityKey: r.IdentityKey, SourceFingerprint: r.SourceFingerprint, ContentDigest: r.ContentDigest, ItemCount: r.ItemCount})
	if !errors.Is(err, ErrAmbiguous) || calls.Load() != 2 {
		t.Fatal("creation outcome lost or retried", err)
	}
}
