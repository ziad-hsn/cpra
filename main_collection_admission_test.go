package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

// Observe only fixed request paths/statuses; never retain headers or bodies.
type mainCollectionAdmissionTransport struct {
	http.RoundTripper
	preparationPosts atomic.Int64
	validation202    atomic.Int64
	activationPosts  atomic.Int64
}

func (transport *mainCollectionAdmissionTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method == http.MethodPost {
		if strings.HasSuffix(request.URL.Path, "/prepare") {
			transport.preparationPosts.Add(1)
		}
		if strings.HasSuffix(request.URL.Path, "/activate") {
			transport.activationPosts.Add(1)
		}
	}
	response, err := transport.RoundTripper.RoundTrip(request)
	if err == nil && request.Method == http.MethodPost {
		if strings.HasSuffix(request.URL.Path, "/validate") && response.StatusCode == http.StatusAccepted {
			transport.validation202.Add(1)
		}
	}
	return response, err
}
func (transport *mainCollectionAdmissionTransport) CloseIdleConnections() {
	if closer, ok := transport.RoundTripper.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// The real SDK explicitly prepares, stages and validates input through normal
// TLS/Raft startup, then reconciles the same original creation ticket after
// owner restart. Activation is a separate available action, never requested by
// this test; successful Apply is covered by its own integration tests.
func TestMainCollectionAdmissionSurvivesRestartWithoutActivation(t *testing.T) {
	fixture := newMainManagementFixture(t)
	transport := &mainCollectionAdmissionTransport{RoundTripper: fixture.client.Transport}
	fixture.client.Transport = transport
	var calls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(200) }))
	defer target.Close()
	marker := "private-staged-collection-canary"
	secret := target.URL + "/" + marker
	objects := []any{
		api.Monitor{APIVersion: api.APIVersion, Kind: "Monitor", Metadata: api.Metadata{ID: "staged-service"}, Spec: api.MonitorSpec{Enabled: api.Pointer(true), Check: api.CheckSpec{Interval: "250ms", Timeout: "1s", Driver: api.DriverConfig{Type: "http", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"url": "staged-target"})}}}},
		api.Credential{APIVersion: api.APIVersion, Kind: "Credential", Metadata: api.Metadata{ID: "staged-target"}, Spec: api.CredentialSpec{Value: &secret}},
	}
	identity, input := mainCollectionAdmissionInput(t, objects)
	client, stop := startMainManagement(t, fixture)
	prepared, err := client.Operations.Prepare(t.Context(), identity)
	if err != nil {
		t.Fatal(err)
	}
	// Retain this exact ticket and frozen input identity across both restarts.
	originalRequest := api.OperationCreateRequest{AdmissionTicket: prepared.Data.Ticket,
		IdentityFormat: api.OperationCreateRequestIdentityFormat(identity.IdentityFormat), IdentityKey: identity.IdentityKey,
		SourceFingerprint: identity.SourceFingerprint, ContentDigest: identity.ContentDigest, ItemCount: identity.ItemCount}
	created, err := client.Operations.Create(t.Context(), originalRequest)
	if err != nil || created.Data.ID == "" {
		t.Fatal("create original inactive operation", err)
	}
	id := created.Data.ID
	assertIdentity := func(receipt api.Operation) {
		t.Helper()
		if receipt.ID != id || receipt.IdentityFormat != commitment.Format || receipt.ContentDigest != identity.ContentDigest ||
			receipt.ItemCount == nil || *receipt.ItemCount != identity.ItemCount || receipt.NormalizationProfile != "" {
			t.Fatal("original collection identity changed")
		}
	}
	assertIdentity(created.Data)
	uploaded, err := client.Operations.Upload(t.Context(), id, input)
	if err != nil || uploaded.Data.Uploaded == nil || *uploaded.Data.Uploaded != 2 || uploaded.Data.State != "uploading" {
		t.Fatal("upload original inactive input", err)
	}
	assertIdentity(uploaded.Data)
	requested, err := client.Operations.Validate(t.Context(), id)
	if err != nil || requested.StatusCode != http.StatusAccepted {
		t.Fatal("request actual asynchronous validation", err)
	}
	assertIdentity(requested.Data)
	validationCtx, cancelValidation := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancelValidation()
	validation, err := client.Operations.WaitValidation(validationCtx, id)
	if err != nil || !validation.Data.Summary.Valid || validation.Data.Summary.PlanID == "" || validation.Data.Summary.PlanDigest == "" ||
		validation.Data.OperationID != id || validation.Data.ContentDigest != identity.ContentDigest || validation.Data.ItemCount != 2 {
		t.Fatal("original staged union did not finish validation", err)
	}
	assertValidatedInactive := func(client *cpra.Client, id string) {
		t.Helper()
		receipt, err := client.Operations.Get(t.Context(), id)
		if err != nil || receipt.Data.State != "validated" || receipt.Data.Validated == nil || !*receipt.Data.Validated || receipt.Data.Uploaded == nil || *receipt.Data.Uploaded != 2 || receipt.Data.Committed == nil || *receipt.Data.Committed != 0 {
			t.Fatal("original sealed validation progress unavailable", err)
		}
		assertIdentity(receipt.Data)
		monitors, err := client.Monitors.List(t.Context(), cpra.ListOptions{Limit: 1})
		if err != nil || len(monitors.Data.Items) != 0 {
			t.Fatal("staging activated monitors", err)
		}
		credentials, err := client.Credentials.List(t.Context(), cpra.ListOptions{Limit: 1})
		if err != nil || len(credentials.Data.Items) != 0 {
			t.Fatal("staging activated credentials", err)
		}
	}
	if transport.preparationPosts.Load() != 1 || transport.validation202.Load() != 1 || transport.activationPosts.Load() != 0 {
		t.Fatal("staging did not use one original preparation and validation without activation")
	}
	assertValidatedInactive(client, id)
	stop()
	fixture.options.webAddr = availableLocalAddress(t)
	restarted, stopRestarted := startMainManagement(t, fixture)
	assertValidatedInactive(restarted, id)
	// Reconcile the original ticket through the real Create endpoint. A retry
	// returns its original validation; it does not prepare or activate anything.
	again, err := restarted.Operations.Create(t.Context(), originalRequest)
	if err != nil || again.Data.State != "validated" {
		t.Fatal("creation retry lost original validation after restart", err)
	}
	assertIdentity(again.Data)
	retainedValidation, err := restarted.Operations.Validation(t.Context(), id, cpra.ValidationPageOptions{})
	if err != nil || !reflect.DeepEqual(retainedValidation.Data, validation.Data) {
		t.Fatal("restart or creation retry replaced original sealed validation", err)
	}
	if transport.preparationPosts.Load() != 1 || transport.validation202.Load() != 1 || transport.activationPosts.Load() != 0 {
		t.Fatal("restart retry readmitted, revalidated or activated the original input")
	}
	assertValidatedInactive(restarted, id)
	canceled, err := restarted.Operations.Cancel(t.Context(), id)
	if err != nil || canceled.Data.State != "canceled" || canceled.Data.Uploaded == nil || *canceled.Data.Uploaded != 2 {
		t.Fatal("cancel original inactive input", err)
	}
	assertIdentity(canceled.Data)
	stopRestarted()
	fixture.options.webAddr = availableLocalAddress(t)
	afterCancel, stopAfterCancel := startMainManagement(t, fixture)
	receipt, err := afterCancel.Operations.Get(t.Context(), id)
	if err != nil || receipt.Data.State != "canceled" || receipt.Data.Committed == nil || *receipt.Data.Committed != 0 {
		t.Fatal("restart lost original inactive cancellation receipt", err)
	}
	assertIdentity(receipt.Data)
	repeated, err := afterCancel.Operations.Cancel(t.Context(), id)
	if err != nil || repeated.Data.State != "canceled" || repeated.Data.Uploaded == nil || *repeated.Data.Uploaded != 2 {
		t.Fatal("restart cancellation retry lost original receipt", err)
	}
	assertIdentity(repeated.Data)
	monitors, err := afterCancel.Monitors.List(t.Context(), cpra.ListOptions{Limit: 1})
	if err != nil || len(monitors.Data.Items) != 0 {
		t.Fatal("cancellation recovery activated a monitor", err)
	}
	credentials, err := afterCancel.Credentials.List(t.Context(), cpra.ListOptions{Limit: 1})
	if err != nil || len(credentials.Data.Items) != 0 {
		t.Fatal("cancellation recovery activated a credential", err)
	}
	stopAfterCancel()
	if calls.Load() != 0 || transport.activationPosts.Load() != 0 {
		t.Fatal("inactive input invoked activation or a provider")
	}
	err = filepath.WalkDir(fixture.settings.Storage.Directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if bytes.Contains(data, []byte(marker)) {
			t.Fatal("plaintext staged configuration entered durable files")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Log("normal TLS startup and owner restarts preserved original collection ticket/handle, two encrypted inactive rows and original cancellation receipt; zero active resources and provider calls")
}

// Build the exact public inventory protocol in memory. These SDK requests let
// the test stop after validation; the higher-level Apply intentionally includes
// activation and is exercised separately.
func mainCollectionAdmissionInput(t *testing.T, objects []any) (api.CollectionPrepareRequest, api.UploadRequest) {
	t.Helper()
	key := make([]byte, commitment.KeyBytes)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	defer clear(key)
	sources, err := commitment.NewSourceAccumulator(key, uint64(len(objects)), 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer sources.Close()
	var upload api.UploadRequest
	var positions []commitment.Position
	var macs [][commitment.MACBytes]byte
	for n, object := range objects {
		raw, err := json.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		token, err := commitment.SourceToken(uint64(n + 1))
		if err != nil {
			t.Fatal(err)
		}
		if err := sources.Begin(token); err != nil {
			t.Fatal(err)
		}
		if _, err := sources.Write(raw); err != nil {
			t.Fatal(err)
		}
		if err := sources.End(); err != nil {
			t.Fatal(err)
		}
		resource, err := api.DecodeResource(raw)
		clear(raw)
		if err != nil {
			t.Fatal(err)
		}
		wire, err := json.Marshal(resource)
		if err != nil {
			t.Fatal(err)
		}
		position := commitment.Position{Ordinal: uint64(n + 1), ID: resource.Kind + "/" + resource.Metadata.ID,
			Source: commitment.SourcePosition{Token: token, Document: 1, Item: 1}}
		mac, err := commitment.ItemMAC(key, position, wire)
		clear(wire)
		if err != nil {
			t.Fatal(err)
		}
		positions, macs = append(positions, position), append(macs, mac)
		upload.Items = append(upload.Items, api.ApplyItem{ID: position.ID, Ordinal: int64(position.Ordinal), Source: token,
			SourceDocument: 1, SourceItem: 1, Resource: resource, ContentDigest: hex.EncodeToString(mac[:])})
	}
	fingerprint, err := sources.Finish()
	if err != nil {
		t.Fatal(err)
	}
	accumulator, err := commitment.NewAccumulator(key, uint64(len(objects)), fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	defer accumulator.Close()
	for i, position := range positions {
		if err := accumulator.Add(position, macs[i]); err != nil {
			t.Fatal(err)
		}
	}
	digest, err := accumulator.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return api.CollectionPrepareRequest{IdentityFormat: commitment.Format, IdentityKey: api.Pointer(hex.EncodeToString(key)),
		SourceFingerprint: api.Pointer(hex.EncodeToString(fingerprint[:])), ContentDigest: hex.EncodeToString(digest[:]), ItemCount: int64(len(objects))}, upload
}
