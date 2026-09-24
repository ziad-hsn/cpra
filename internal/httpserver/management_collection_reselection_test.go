package httpserver

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/fleetview"
	"github.com/ziad-hsn/cpra/internal/httpauth"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

const reselectionOtherToken = "another-operator-fixture-01234567890123456789"
const reselectionHTTPSecret = "PRIVATE-RESELECTION-HTTP-CREDENTIAL"

type reselectionHTTPFixture struct {
	*managementFixture
	bodyReads atomic.Int64
}
type reselectionHTTPBody struct {
	io.ReadCloser
	reads *atomic.Int64
}

func (b reselectionHTTPBody) Read(p []byte) (int, error) { b.reads.Add(1); return b.ReadCloser.Read(p) }

func newReselectionHTTPFixture(t *testing.T) *reselectionHTTPFixture {
	t.Helper()
	base := newManagementFixture(t, true)
	base.http.Close()
	hash, err := httpauth.HashToken(reselectionOtherToken)
	if err != nil {
		t.Fatal(err)
	}
	base.authConfig.Principals = append(base.authConfig.Principals, httpauth.Principal{ID: "other-operator", Role: httpauth.Operator, TokenSHA256: hash})
	validationHTTPAuthority(t, base)
	parent := t.TempDir()
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	manager, err := management.NewCollectionReselectionManager(t.Context(), base.catalog, parent, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		manager.BeginStop()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := manager.Wait(ctx); err != nil {
			t.Error(err)
		}
	})
	auth, err := httpauth.New(base.authConfig)
	if err != nil {
		t.Fatal(err)
	}
	base.server = New(ServerConfig{Store: base.server.cfg.Store, Management: base.catalog, ManagementAuth: auth, Reselection: manager, AuthToken: managementLegacyToken, Ready: func() bool { return true }}, fleetview.NewHolder(), nil, nil, nil, nil, nil, nil, nil, PublicConfig{})
	mux := http.NewServeMux()
	base.server.registerAPI(mux)
	base.server.registerMetrics(mux)
	base.server.registerSPA(mux)
	f := &reselectionHTTPFixture{managementFixture: base}
	handler := base.server.corsMiddleware(base.server.authMiddleware(mux))
	base.http = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test-Count-Body") == "yes" {
			r.Body = reselectionHTTPBody{r.Body, &f.bodyReads}
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(base.http.Close)
	base.sdk = f.freshClient(t)
	return f
}
func (f *reselectionHTTPFixture) freshClient(t *testing.T) *cpra.Client {
	t.Helper()
	c, err := cpra.New(cpra.Config{BaseURL: f.http.URL, AuthToken: managementOperatorToken, HTTPClient: f.http.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

type reselectionHTTPInput struct {
	operation api.Operation
	raw       [][]byte
	identity  api.CollectionPrepareRequest
	prefix    []persistence.CollectionItem
	header    persistence.CollectionState
}

func (f *reselectionHTTPFixture) seed(t *testing.T, target string) reselectionHTTPInput {
	t.Helper()
	monitor, _ := json.Marshal(preflightMonitor("app", target))
	input := reselectionHTTPInput{raw: [][]byte{[]byte("# PRIVATE-SOURCE-PATH service/secret.yaml\napiVersion: cpra.io/v2\nkind: Credential\nmetadata:\n  id: service-token\nspec:\n  value: " + reselectionHTTPSecret + "\n"), monitor, {}}}
	key := bytes.Repeat([]byte{71}, commitment.KeyBytes)
	defer clear(key)
	sources, err := commitment.NewSourceAccumulator(key, uint64(len(input.raw)), 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer sources.Close()
	var items []api.ApplyItem
	var positions []commitment.Position
	var macs [][32]byte
	for i, raw := range input.raw {
		token, err := commitment.SourceToken(uint64(i + 1))
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
		err = collection.NormalizeFile(t.Context(), bytes.NewReader(raw), collection.FileNormalizationProfile, collection.DecodeOptions{}, func(item collection.NormalizedItem) error {
			resource, err := api.DecodeResource(item.JSON)
			if err != nil {
				return err
			}
			pos := commitment.Position{Ordinal: uint64(len(items) + 1), ID: item.ID, Source: commitment.SourcePosition{Token: token, Document: uint64(item.Location.Document), Item: uint64(item.Location.Item)}}
			mac, err := commitment.ItemMAC(key, pos, item.JSON)
			if err != nil {
				return err
			}
			items = append(items, api.ApplyItem{ID: item.ID, Ordinal: int64(pos.Ordinal), Resource: resource, Source: token, SourceDocument: int64(pos.Source.Document), SourceItem: int64(pos.Source.Item), ContentDigest: hex.EncodeToString(mac[:])})
			positions = append(positions, pos)
			macs = append(macs, mac)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	fingerprint, err := sources.Finish()
	if err != nil {
		t.Fatal(err)
	}
	acc, err := commitment.NewAccumulator(key, uint64(len(items)), fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	defer acc.Close()
	for i, pos := range positions {
		if err := acc.Add(pos, macs[i]); err != nil {
			t.Fatal(err)
		}
	}
	digest, err := acc.Finish()
	if err != nil {
		t.Fatal(err)
	}
	input.identity = api.CollectionPrepareRequest{IdentityFormat: api.CollectionPrepareRequestIdentityFormat(commitment.Format), IdentityKey: api.Pointer(hex.EncodeToString(key)), SourceFingerprint: api.Pointer(hex.EncodeToString(fingerprint[:])), ContentDigest: hex.EncodeToString(digest[:]), ItemCount: int64(len(items)), NormalizationProfile: collection.FileNormalizationProfile}
	ticket, err := f.sdk.Operations.Prepare(t.Context(), input.identity)
	if err != nil {
		t.Fatal("prepare", err)
	}
	req := api.OperationCreateRequest{AdmissionTicket: ticket.Data.Ticket, IdentityFormat: api.OperationCreateRequestIdentityFormat(input.identity.IdentityFormat), IdentityKey: input.identity.IdentityKey, SourceFingerprint: input.identity.SourceFingerprint, ContentDigest: input.identity.ContentDigest, ItemCount: input.identity.ItemCount, NormalizationProfile: input.identity.NormalizationProfile}
	created, err := f.sdk.Operations.Create(t.Context(), req)
	if err != nil {
		t.Fatal("create", err)
	}
	uploaded, err := f.sdk.Operations.Upload(t.Context(), created.Data.ID, api.UploadRequest{Items: items[:1]})
	if err != nil {
		t.Fatal("seed upload", err)
	}
	input.operation = uploaded.Data
	input.header, _, err = f.server.cfg.Store.CollectionGet(created.Data.ID)
	if err != nil {
		t.Fatal(err)
	}
	input.prefix, err = f.server.cfg.Store.CollectionPage(created.Data.ID, 0, 1)
	if err != nil || len(input.prefix) != 1 {
		t.Fatal("prefix", err)
	}
	return input
}
func (f *reselectionHTTPFixture) attempt(t *testing.T, input reselectionHTTPInput) api.CollectionReselectionAttempt {
	t.Helper()
	response, err := f.freshClient(t).Operations.CreateReselection(t.Context(), input.operation.ID, api.CollectionReselectionCreateRequest{SourceCount: int64(len(input.raw)), NormalizationProfile: collection.FileNormalizationProfile})
	if err != nil {
		t.Fatal("create attempt", err)
	}
	if response.StatusCode != http.StatusCreated || response.Data.OperationUploaded != 1 {
		t.Fatal("original prefix omitted")
	}
	return response.Data
}
func (f *reselectionHTTPFixture) upload(t *testing.T, input reselectionHTTPInput, a api.CollectionReselectionAttempt) {
	t.Helper()
	client := f.freshClient(t)
	for i, raw := range input.raw {
		split := len(raw) / 2
		if split > 0 {
			if _, err := client.Operations.UploadReselectionSource(t.Context(), input.operation.ID, a.ID, cpra.ReselectionSourcePart{Source: int64(i + 1), Data: raw[:split]}); err != nil {
				t.Fatal("first part", err)
			}
		}
		r, err := client.Operations.UploadReselectionSource(t.Context(), input.operation.ID, a.ID, cpra.ReselectionSourcePart{Source: int64(i + 1), Offset: int64(split), End: true, Data: raw[split:]})
		if err != nil {
			t.Fatal("end part", err)
		}
		if r.Data.SourcesCompleted != int64(i+1) {
			t.Fatal("source progress missing")
		}
	}
}
func (f *reselectionHTTPFixture) wait(t *testing.T, input reselectionHTTPInput, a api.CollectionReselectionAttempt, phase string) api.CollectionReselectionAttempt {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for {
		r, err := f.sdk.Operations.GetReselection(ctx, input.operation.ID, a.ID)
		if err != nil {
			t.Fatal("poll", err)
		}
		if string(r.Data.Phase) == phase {
			return r.Data
		}
		if r.Data.Phase == "failed" {
			t.Fatal("unexpected failed attempt", r.Data.ErrorCode)
		}
		select {
		case <-ctx.Done():
			t.Fatal("attempt did not finish")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func (f *reselectionHTTPFixture) unchanged(t *testing.T, input reselectionHTTPInput) {
	t.Helper()
	head, _, err := f.server.cfg.Store.CollectionGet(input.operation.ID)
	if err != nil || !reflect.DeepEqual(head, input.header) {
		t.Fatal("original header changed", err)
	}
	prefix, err := f.server.cfg.Store.CollectionPage(input.operation.ID, 0, 256)
	if err != nil || !reflect.DeepEqual(prefix, input.prefix) {
		t.Fatal("original ciphertext changed", err)
	}
}
func (f *reselectionHTTPFixture) noActivation(t *testing.T) {
	t.Helper()
	view, err := f.server.cfg.Store.CatalogSnapshot()
	if err != nil || view.Len() != 0 {
		t.Fatal("reselection activated configuration", err)
	}
}
func reselectionNoPrivateResponse(t *testing.T, raw []byte, input reselectionHTTPInput) {
	t.Helper()
	for _, private := range []string{reselectionHTTPSecret, "PRIVATE-SOURCE-PATH", "service/secret.yaml", *input.identity.IdentityKey, *input.identity.SourceFingerprint, input.identity.ContentDigest, `"identityKey"`, `"sourceFingerprint"`, `"contentDigest"`, `"ticket"`, `"resource"`, `"filename"`} {
		if bytes.Contains(raw, []byte(private)) {
			t.Fatal("private reselection input returned")
		}
	}
}

func TestCollectionReselectionHTTPRealSDKPreservesPrefixAndDoesNotActivate(t *testing.T) {
	f := newReselectionHTTPFixture(t)
	var calls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer target.Close()
	input := f.seed(t, target.URL)
	a := f.attempt(t, input)
	f.upload(t, input, a)
	verified, err := f.freshClient(t).Operations.VerifyReselection(t.Context(), input.operation.ID, a.ID)
	if err != nil || verified.StatusCode != 202 {
		t.Fatal("verify admission", err)
	}
	f.wait(t, input, a, "verified")
	f.unchanged(t, input)
	resumed, err := f.freshClient(t).Operations.ResumeReselection(t.Context(), input.operation.ID, a.ID)
	if err != nil || resumed.StatusCode != 202 {
		t.Fatal("resume admission", err)
	}
	completed := f.wait(t, input, a, "completed")
	if completed.OperationID != input.operation.ID || completed.OperationUploaded != 2 || completed.SourcesCompleted != 3 || completed.NextSource != 0 || completed.NextOffset != 0 || !completed.ExpiresAt.Equal(a.ExpiresAt) {
		t.Fatal("completion lost original identity or renewed expiry")
	}
	head, _, err := f.server.cfg.Store.CollectionGet(input.operation.ID)
	if err != nil || head.Uploaded != 2 || head.ID != input.header.ID || head.UploadID != input.header.UploadID || head.Phase != "uploading" {
		t.Fatal("resumption replaced or activated operation", err)
	}
	prefix, err := f.server.cfg.Store.CollectionPage(input.operation.ID, 0, 1)
	if err != nil || !reflect.DeepEqual(prefix, input.prefix) {
		t.Fatal("committed prefix ciphertext replaced", err)
	}
	response, raw := f.request(t, http.MethodGet, "/api/v2/operations/"+input.operation.ID+"/reselection/"+a.ID, managementOperatorToken, nil, nil)
	if response.TLS == nil || response.StatusCode != 200 {
		t.Fatal("expected real TLS progress")
	}
	reselectionNoPrivateResponse(t, raw, input)
	f.noActivation(t)
	if calls.Load() != 0 {
		t.Fatal("proof invoked provider")
	}
	if r, err := f.sdk.Operations.DiscardReselection(t.Context(), input.operation.ID, a.ID); err != nil || r.StatusCode != 204 {
		t.Fatal("discard", err)
	}
	if _, err := f.sdk.Operations.GetReselection(t.Context(), input.operation.ID, a.ID); !errors.Is(err, cpra.ErrNotFound) {
		t.Fatal("discarded attempt remains readable", err)
	}
	after, _, err := f.server.cfg.Store.CollectionGet(input.operation.ID)
	if err != nil || !reflect.DeepEqual(head, after) {
		t.Fatal("discard changed original operation", err)
	}
}

func TestCollectionReselectionHTTPChangedRawFailsWithoutAppend(t *testing.T) {
	f := newReselectionHTTPFixture(t)
	input := f.seed(t, "https://example.test/health")
	a := f.attempt(t, input)
	changed := input
	changed.raw = append([][]byte(nil), input.raw...)
	changed.raw[0] = append(bytes.Clone(changed.raw[0]), []byte("# changed comment only\n")...)
	f.upload(t, changed, a)
	if _, err := f.sdk.Operations.VerifyReselection(t.Context(), input.operation.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	result := f.wait(t, input, a, "failed")
	if result.ErrorCode == nil || *result.ErrorCode != "input_mismatch" {
		t.Fatal("changed raw source not identified safely", result.ErrorCode)
	}
	raw, _ := json.Marshal(result)
	reselectionNoPrivateResponse(t, raw, input)
	f.unchanged(t, input)
	f.noActivation(t)
}

func TestCollectionReselectionHTTPAuthorizationBeforeBody(t *testing.T) {
	f := newReselectionHTTPFixture(t)
	input := f.seed(t, "https://example.test/health")
	a := f.attempt(t, input)
	base := "/api/v2/operations/" + input.operation.ID + "/reselection"
	for _, tc := range []struct {
		name, token string
		status      int
	}{{"no-token", "", 401}, {"reader", managementReaderToken, 403}, {"other-owner", reselectionOtherToken, 404}} {
		for _, route := range []struct {
			name, method, path, media string
			body                      []byte
		}{{"create", "POST", base, "application/json", []byte(`{"sourceCount":3,"normalizationProfile":"cpra.file.base.v1"}`)}, {"source", "PUT", base + "/" + a.ID + "/sources/1?offset=0&end=true", "application/octet-stream", input.raw[0]}} {
			t.Run(tc.name+"/"+route.name, func(t *testing.T) {
				f.bodyReads.Store(0)
				r, raw := f.request(t, route.method, route.path, tc.token, route.body, map[string]string{"Content-Type": route.media, "X-Test-Count-Body": "yes"})
				if r.StatusCode != tc.status || f.bodyReads.Load() != 0 {
					t.Fatalf("denial read source or wrong status: status=%d reads=%d", r.StatusCode, f.bodyReads.Load())
				}
				if r.Header.Get("X-Operation-ID") != "" || bytes.Contains(raw, []byte(a.ID)) {
					t.Fatal("denial exposed owned handle")
				}
				reselectionNoPrivateResponse(t, raw, input)
			})
		}
	}
	f.unchanged(t, input)
}

func TestCollectionReselectionHTTPBodyAndQueryBounds(t *testing.T) {
	f := newReselectionHTTPFixture(t)
	input := f.seed(t, "https://example.test/health")
	a := f.attempt(t, input)
	base := "/api/v2/operations/" + input.operation.ID + "/reselection"
	for _, tc := range []struct {
		name, method, path, media string
		body                      []byte
		status                    int
	}{
		{"oversize", "PUT", base + "/" + a.ID + "/sources/1?offset=0&end=true", "application/octet-stream", bytes.Repeat([]byte{'a'}, (1<<20)+1), 413},
		{"duplicate-offset", "PUT", base + "/" + a.ID + "/sources/1?offset=0&offset=0&end=true", "application/octet-stream", input.raw[0], 422},
		{"duplicate-end", "PUT", base + "/" + a.ID + "/sources/1?offset=0&end=true&end=false", "application/octet-stream", input.raw[0], 422},
		{"noncanonical-source", "PUT", base + "/" + a.ID + "/sources/01?offset=0&end=true", "application/octet-stream", input.raw[0], 422},
		{"encoded-source", "PUT", base + "/" + a.ID + "/sources/1?offset=0&end=true", "application/json", input.raw[0], 415},
		{"create-media", "POST", base, "application/octet-stream", []byte(`{"sourceCount":3,"normalizationProfile":"cpra.file.base.v1"}`), 415},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, raw := f.request(t, tc.method, tc.path, managementOperatorToken, tc.body, map[string]string{"Content-Type": tc.media})
			if r.StatusCode != tc.status {
				t.Fatalf("expected %d got %d", tc.status, r.StatusCode)
			}
			reselectionNoPrivateResponse(t, raw, input)
		})
	}
	r, err := f.sdk.Operations.GetReselection(t.Context(), input.operation.ID, a.ID)
	if err != nil || r.Data.RawBytes != 0 || r.Data.SourcesCompleted != 0 {
		t.Fatal("rejected source changed staging", err)
	}
	f.unchanged(t, input)
}
