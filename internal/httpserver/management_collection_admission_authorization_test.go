package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/fleetview"
	"github.com/ziad-hsn/cpra/internal/httpauth"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// The fixture gates the actual production sealer's key-wrap call. It neither
// replaces ciphertext nor changes admission/authorization code. The binding
// selects a specific boundary without depending on unrelated Wrap call counts.
type collectionAuthorizationWrapGate struct {
	secureconfig.KeyWrapper
	kind, revisionPrefix string
	entered              chan struct{}
	released             chan struct{}
	enterOnce, closeOnce sync.Once
}

func (g *collectionAuthorizationWrapGate) release() { g.closeOnce.Do(func() { close(g.released) }) }

func (g *collectionAuthorizationWrapGate) Wrap(ctx context.Context, key, aad []byte) ([]byte, error) {
	var metadata struct {
		Binding secureconfig.Binding `json:"binding"`
	}
	if err := json.Unmarshal(aad, &metadata); err != nil {
		return nil, errors.New("invalid key-wrap fixture binding")
	}
	if metadata.Binding.Kind == g.kind && strings.HasPrefix(metadata.Binding.Revision, g.revisionPrefix) {
		g.enterOnce.Do(func() { close(g.entered) })
		select {
		case <-g.released:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return g.KeyWrapper.Wrap(ctx, key, aad)
}

func newCollectionAuthorizationFixture(t *testing.T, kind, revision string) (*managementFixture, *persistence.Store, *httpauth.Authorizer, *collectionAuthorizationWrapGate) {
	t.Helper()
	cfg := runtimeconfig.Default()
	cfg.Storage.Mode = "memory"
	store, err := persistence.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	inner, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{0x38}, 32))
	if err != nil {
		t.Fatal(err)
	}
	gate := &collectionAuthorizationWrapGate{KeyWrapper: inner, kind: kind, revisionPrefix: revision, entered: make(chan struct{}), released: make(chan struct{})}
	sealer, err := secureconfig.NewSealer(gate)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := management.NewCatalog(store, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if err = catalog.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	hash, err := httpauth.HashToken(managementOperatorToken)
	if err != nil {
		t.Fatal(err)
	}
	policy := httpauth.Config{Principals: []httpauth.Principal{{ID: "operator", Role: httpauth.Operator, TokenSHA256: hash}}}
	auth, err := httpauth.New(policy)
	if err != nil {
		t.Fatal(err)
	}
	server := New(ServerConfig{Store: store, Management: catalog, ManagementAuth: auth, Ready: func() bool { return true }}, fleetview.NewHolder(), nil, nil, nil, nil, nil, nil, nil, PublicConfig{})
	mux := http.NewServeMux()
	server.registerAPI(mux)
	transport := httptest.NewTLSServer(server.corsMiddleware(server.authMiddleware(mux)))
	t.Cleanup(transport.Close)
	// Release before httptest.Close even when an assertion fails while blocked.
	t.Cleanup(gate.release)
	return &managementFixture{server: server, http: transport, catalog: catalog, authConfig: policy}, store, auth, gate
}

type collectionAuthorizationHTTPResult struct {
	status      int
	body        []byte
	tls         bool
	err         error
	operationID string
}

func startCollectionAuthorizationRequest(t *testing.T, f *managementFixture, method, path string, input any) <-chan collectionAuthorizationHTTPResult {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(method, f.http.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+managementOperatorToken)
	req.Header.Set("Content-Type", "application/json")
	result := make(chan collectionAuthorizationHTTPResult, 1)
	go func() {
		defer clear(raw)
		response, err := f.http.Client().Do(req)
		if err != nil {
			result <- collectionAuthorizationHTTPResult{err: err}
			return
		}
		defer response.Body.Close()
		body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		result <- collectionAuthorizationHTTPResult{status: response.StatusCode, body: body, tls: response.TLS != nil, err: err, operationID: response.Header.Get("X-Operation-ID")}
	}()
	return result
}

func TestCollectionAdmissionHTTPRejectsPrincipalRemapBeforeResponse(t *testing.T) {
	f, store, auth, gate := newCollectionAuthorizationFixture(t, "CollectionAdmission", "1")
	frozen := preflightInventory(t, preflightMonitor("private", "https://private.example.test"))
	identity := collectionHTTPIdentity(frozen)
	pending := startCollectionAuthorizationRequest(t, f, http.MethodPost, "/api/v2/collections/prepare", identity)
	select {
	case <-gate.entered:
	case result := <-pending:
		t.Fatalf("request returned before ticket wrapping: HTTP %d err=%v", result.status, result.err)
	case <-time.After(5 * time.Second):
		t.Fatal("ticket wrapping was never reached")
	}
	// The epoch is already admitted. Remapping the same valid bearer here tests
	// response publication, independently of the earlier mutation admission.
	policy := f.authConfig
	policy.Principals = append([]httpauth.Principal(nil), policy.Principals...)
	policy.Principals[0].ID = "different-operator"
	if err := auth.Replace(policy); err != nil {
		t.Fatal(err)
	}
	gate.release()
	select {
	case result := <-pending:
		if result.err != nil || !result.tls || result.status != http.StatusForbidden || result.operationID != "" {
			t.Fatalf("remapped principal received original response: HTTP %d TLS=%v err=%v", result.status, result.tls, result.err)
		}
		for _, private := range []string{"\"ticket\"", *frozen.IdentityKey, *identity.SourceFingerprint, identity.ContentDigest} {
			if bytes.Contains(result.body, []byte(private)) {
				t.Fatal("response to remapped principal disclosed private input or ticket")
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request did not finish after ticket wrapping")
	}
	catalog, err := store.CatalogSnapshot()
	if err != nil || catalog.Len() != 0 {
		t.Fatal("preparation changed active configuration", err)
	}
	epoch, err := store.OperationEpoch()
	if err != nil || epoch == "" {
		t.Fatal("fixture did not admit an epoch before ticket wrapping", err)
	}
	_, exists, err := store.CollectionGet(fmt.Sprintf("op.%s.%020d", epoch, 1))
	if err != nil || exists {
		t.Fatal("preparation allocated an operation", err)
	}
}

func TestCollectionAdmissionHTTPRechecksAuthorizationAfterKeyWrap(t *testing.T) {
	for _, step := range []string{"create", "second-upload-row"} {
		for _, transition := range []string{"revoke", "expire"} {
			t.Run(step+"/"+transition, func(t *testing.T) {
				kind, revision := "Collection", ""
				if step == "second-upload-row" {
					kind, revision = "CollectionItem", "2:"
				}
				f, store, auth, gate := newCollectionAuthorizationFixture(t, kind, revision)
				frozen := preflightInventory(t, preflightMonitor("first", "https://first.example.test"), preflightMonitor("second", "https://second.example.test"))
				identity := collectionHTTPIdentity(frozen)
				raw, err := json.Marshal(identity)
				if err != nil {
					t.Fatal(err)
				}
				response, body := f.request(t, http.MethodPost, "/api/v2/collections/prepare", managementOperatorToken, raw, nil)
				var ticket api.CollectionAdmission
				if response.StatusCode != http.StatusOK || json.Unmarshal(body, &ticket) != nil || ticket.Ticket == "" {
					t.Fatalf("prepare failed: HTTP %d", response.StatusCode)
				}
				create := api.OperationCreateRequest{AdmissionTicket: ticket.Ticket, IdentityFormat: api.OperationCreateRequestIdentityFormat(identity.IdentityFormat), IdentityKey: identity.IdentityKey, SourceFingerprint: identity.SourceFingerprint, ContentDigest: identity.ContentDigest, ItemCount: identity.ItemCount}
				method, path, input := http.MethodPost, "/api/v2/operations", any(create)
				var original api.Operation
				if step == "second-upload-row" {
					raw, err = json.Marshal(create)
					if err != nil {
						t.Fatal(err)
					}
					response, body = f.request(t, http.MethodPost, path, managementOperatorToken, raw, nil)
					if response.StatusCode != http.StatusOK || json.Unmarshal(body, &original) != nil || original.ID == "" {
						t.Fatalf("setup create failed: HTTP %d", response.StatusCode)
					}
					method, path, input = http.MethodPut, "/api/v2/operations/"+original.ID+"/items", api.UploadRequest{Items: frozen.Items}
				}
				var expires time.Time
				if transition == "expire" {
					// Exercise real wall-clock expiration, not a fake authorizer clock.
					expires = time.Now().Add(2 * time.Second)
					policy := f.authConfig
					policy.Principals = append([]httpauth.Principal(nil), policy.Principals...)
					policy.Principals[0].ExpiresAt = expires
					if err := auth.Replace(policy); err != nil {
						t.Fatal(err)
					}
				}
				before := store.Status().CommittedIndex
				pending := startCollectionAuthorizationRequest(t, f, method, path, input)
				select {
				case <-gate.entered:
				case result := <-pending:
					t.Fatalf("request returned before target key wrap: HTTP %d, transport error %v", result.status, result.err)
				case <-time.After(5 * time.Second):
					t.Fatal("key wrap was never reached")
				}
				var prefix persistence.CollectionState
				if step == "second-upload-row" {
					var exists bool
					prefix, exists, err = store.CollectionGet(original.ID)
					if err != nil || !exists || prefix.Uploaded != 1 {
						t.Fatalf("gate did not follow exactly one committed row: exists=%v uploaded=%d err=%v", exists, prefix.Uploaded, err)
					}
				}
				if transition == "revoke" {
					policy := f.authConfig
					policy.Principals = append([]httpauth.Principal(nil), policy.Principals...)
					policy.Principals[0].Revoked = true
					replaced := make(chan error, 1)
					go func() { replaced <- auth.Replace(policy) }()
					select {
					case err := <-replaced:
						if err != nil {
							t.Fatal(err)
						}
					case <-time.After(time.Second):
						t.Fatal("revocation waited for blocked key wrapping; policy lock held across external I/O")
					}
				} else {
					if delay := time.Until(expires); delay > 0 {
						<-time.After(delay)
					}
					if time.Now().Before(expires) {
						t.Fatal("principal expiration did not elapse")
					}
				}
				select {
				case <-gate.released:
					t.Fatal("key wrapping gate was released before authorization changed")
				default:
				}
				gate.release()
				select {
				case result := <-pending:
					if result.err != nil || !result.tls || result.status != http.StatusUnauthorized {
						t.Fatalf("post-wrap authorization was not rejected over TLS: HTTP %d TLS=%v err=%v", result.status, result.tls, result.err)
					}
					if bytes.Contains(result.body, []byte(ticket.Ticket)) || bytes.Contains(result.body, []byte(*frozen.IdentityKey)) {
						t.Fatal("authorization error disclosed private input")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("request did not finish after gate release")
				}
				catalog, err := store.CatalogSnapshot()
				if err != nil || catalog.Len() != 0 {
					t.Fatal("authorization failure changed active configuration", err)
				}
				if step == "create" {
					epoch, err := store.OperationEpoch()
					if err != nil {
						t.Fatal(err)
					}
					_, exists, err := store.CollectionGet(fmt.Sprintf("op.%s.%020d", epoch, 1))
					if err != nil || exists || store.Status().CommittedIndex != before {
						t.Fatalf("unauthorized create committed state: exists=%v err=%v", exists, err)
					}
				} else {
					after, exists, err := store.CollectionGet(original.ID)
					if err != nil || !exists || after.Uploaded != 1 || after.ProgressDigest != prefix.ProgressDigest || after.EncodedBytes != prefix.EncodedBytes {
						t.Fatal("authorization failure changed the committed upload prefix", err)
					}
					rows, err := store.CollectionPage(original.ID, 0, 256)
					if err != nil || len(rows) != 1 || rows[0].Ordinal != 1 {
						t.Fatal("uncommitted second row entered the materialized ledger", err)
					}
				}
			})
		}
	}
}
