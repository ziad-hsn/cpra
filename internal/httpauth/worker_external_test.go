//go:build externaljobs

package httpauth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

const workerToken = "worker-fixture-012345678901234567890123456789"

type workerReaderFunc func(context.Context, string, time.Time) (persistence.WorkerAuthority, error)

func (f workerReaderFunc) AuthenticateWorker(ctx context.Context, hash string, at time.Time) (persistence.WorkerAuthority, error) {
	return f(ctx, hash, at)
}

type unreadWorkerBody struct{ reads atomic.Int32 }

func (b *unreadWorkerBody) Read([]byte) (int, error) {
	b.reads.Add(1)
	return 0, errors.New("authentication must not read a protocol body")
}
func (*unreadWorkerBody) Close() error { return nil }

func TestWorkerAuthenticationProtocolInventory(t *testing.T) {
	a := newFixture(t, configFixture(t))
	at := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return at }
	var schema struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(api.Schema(), &schema); err != nil {
		t.Fatal(err)
	}
	count := 0
	for path, methods := range schema.Paths {
		if !strings.HasPrefix(path, "/api/v2/external-workers/") {
			continue
		}
		for method, op := range methods {
			if op.OperationID == "" {
				continue
			}
			count++
			t.Run(op.OperationID, func(t *testing.T) {
				body := &unreadWorkerBody{}
				r := requestFixture(strings.ToUpper(method), path, workerToken)
				r.Body = body
				want := persistence.WorkerAuthority{WorkerID: "worker-a", WorkerUID: "uid-a", Epoch: "epoch-a", CredentialRevision: "credential-a", GrantRevision: "grant-a", PolicyRevision: "policy-a"}
				calls := 0
				reader := workerReaderFunc(func(ctx context.Context, hash string, observed time.Time) (persistence.WorkerAuthority, error) {
					calls++
					if hash != verifier(t, workerToken) || observed != at || ctx != r.Context() {
						t.Fatal("credential digest, context or observation time changed")
					}
					return want, nil
				})
				got, err := a.AuthorizeWorker(r, op.OperationID, reader)
				if err != nil || got != want || calls != 1 || body.reads.Load() != 0 {
					t.Fatal("worker authentication did not preserve its observation", err)
				}
				if _, err := a.Authorize(r, op.OperationID); !errors.Is(err, ErrForbidden) {
					t.Fatal("worker protocol inherited management authorization")
				}
			})
		}
	}
	if count != 5 {
		t.Fatalf("review the worker authorization inventory: got %d operations", count)
	}
}

func TestWorkerAuthenticationRejectsBeforePolicyRead(t *testing.T) {
	config := configFixture(t)
	config.TrustedProxy = &ProxyConfig{PublicOrigin: "https://cpra.example"}
	a := newFixture(t, config)
	for _, tc := range []struct {
		name string
		edit func(*http.Request)
		op   string
		want error
	}{
		{"missing bearer", func(r *http.Request) { r.Header.Del("Authorization") }, "WorkerStart", ErrWorkerAuthentication},
		{"duplicate bearer", func(r *http.Request) { r.Header.Add("Authorization", "Bearer "+workerToken) }, "WorkerStart", ErrWorkerAuthentication},
		{"basic", func(r *http.Request) { r.SetBasicAuth("cpra", workerToken) }, "WorkerStart", ErrWorkerAuthentication},
		{"short bearer", func(r *http.Request) { r.Header.Set("Authorization", "Bearer short") }, "WorkerStart", ErrWorkerAuthentication},
		{"oversized bearer", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+strings.Repeat("x", MaxTokenBytes+1)) }, "WorkerStart", ErrWorkerAuthentication},
		{"extra separator", func(r *http.Request) { r.Header.Set("Authorization", "Bearer  "+workerToken) }, "WorkerStart", ErrWorkerAuthentication},
		{"operator bearer", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+operatorToken) }, "WorkerStart", ErrWorkerAuthentication},
		{"reader bearer", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+readerToken) }, "WorkerStart", ErrWorkerAuthentication},
		{"legacy bearer", func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+legacyToken) }, "WorkerStart", ErrWorkerAuthentication},
		{"HTTP", func(r *http.Request) { r.TLS = nil }, "WorkerStart", ErrWorkerForbidden},
		{"untrusted proxy", func(r *http.Request) {
			r.TLS = nil
			r.RemoteAddr = "192.0.2.1:54321"
			r.Header.Set("X-Forwarded-Proto", "https")
		}, "WorkerStart", ErrWorkerForbidden},
		{"duplicate forwarding", func(r *http.Request) {
			r.TLS = nil
			r.RemoteAddr = "127.0.0.1:54321"
			r.Header.Add("X-Forwarded-Proto", "https")
			r.Header.Add("X-Forwarded-Proto", "https")
		}, "WorkerStart", ErrWorkerForbidden},
		{"cross origin", func(r *http.Request) { r.Header.Set("Origin", "https://other.example") }, "WorkerStart", ErrWorkerForbidden},
		{"opaque origin", func(r *http.Request) { r.Header.Set("Origin", "null") }, "WorkerStart", ErrWorkerForbidden},
		{"duplicate origin", func(r *http.Request) {
			r.Header.Add("Origin", "https://cpra.example")
			r.Header.Add("Origin", "https://cpra.example")
		}, "WorkerStart", ErrWorkerForbidden},
		{"missing media", func(r *http.Request) { r.Header.Del("Content-Type") }, "WorkerStart", ErrWorkerForbidden},
		{"form media", func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") }, "WorkerStart", ErrWorkerForbidden},
		{"duplicate media", func(r *http.Request) { r.Header.Add("Content-Type", "application/json") }, "WorkerStart", ErrWorkerForbidden},
		{"wrong method", func(r *http.Request) { r.Method = http.MethodGet }, "WorkerStart", ErrWorkerForbidden},
		{"wrong operation", func(*http.Request) {}, "CreateMonitor", ErrWorkerForbidden},
		{"wrong path", func(r *http.Request) { r.URL.Path = "/api/v2/external-workers/result" }, "WorkerStart", ErrWorkerForbidden},
		{"path suffix", func(r *http.Request) { r.URL.Path += "/" }, "WorkerStart", ErrWorkerForbidden},
		{"encoded path", func(r *http.Request) { r.URL.RawPath = "/api/v2/external-workers/%73tart" }, "WorkerStart", ErrWorkerForbidden},
		{"query token", func(r *http.Request) { r.URL.RawQuery = "token=" + workerToken }, "WorkerStart", ErrWorkerForbidden},
		{"empty query", func(r *http.Request) { r.URL.ForceQuery = true }, "WorkerStart", ErrWorkerForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := requestFixture(http.MethodPost, "/api/v2/external-workers/start", workerToken)
			body := &unreadWorkerBody{}
			r.Body = body
			tc.edit(r)
			reader := workerReaderFunc(func(context.Context, string, time.Time) (persistence.WorkerAuthority, error) {
				t.Fatal("invalid request reached committed policy")
				return persistence.WorkerAuthority{}, nil
			})
			got, err := a.AuthorizeWorker(r, tc.op, reader)
			var typed *WorkerError
			if !errors.Is(err, tc.want) || !errors.As(err, &typed) || typed.StatusCode() < 400 || got != (persistence.WorkerAuthority{}) || body.reads.Load() != 0 {
				t.Fatal("incorrect worker denial", err)
			}
			if strings.Contains(err.Error(), workerToken) || strings.Contains(err.Error(), verifier(t, workerToken)) {
				t.Fatal("authentication error exposed credential material")
			}
		})
	}
}

func TestWorkerAuthenticationProxyContract(t *testing.T) {
	for _, proxy := range []bool{false, true} {
		config := configFixture(t)
		if proxy {
			config.TrustedProxy = &ProxyConfig{PublicOrigin: "https://cpra.example"}
		}
		a := newFixture(t, config)
		for _, peer := range []string{"127.0.0.1:54321", "[::1]:54321"} {
			r := requestFixture(http.MethodPost, "/api/v2/external-workers/start", workerToken)
			r.TLS = nil
			r.RemoteAddr = peer
			r.Header.Set("X-Forwarded-Proto", "https")
			r.Header.Set("X-Forwarded-Host", "ignored.example")
			r.Header.Set("Origin", "https://cpra.example")
			calls := 0
			reader := workerReaderFunc(func(context.Context, string, time.Time) (persistence.WorkerAuthority, error) {
				calls++
				return persistence.WorkerAuthority{WorkerID: "worker-a"}, nil
			})
			_, err := a.AuthorizeWorker(r, "WorkerStart", reader)
			if proxy && (err != nil || calls != 1) || !proxy && (!errors.Is(err, ErrWorkerForbidden) || calls != 0) {
				t.Fatal("proxy authorization did not match explicit configuration", err)
			}
		}
	}
}

func TestWorkerAuthenticationPolicyErrorsAndCancellation(t *testing.T) {
	a := newFixture(t, configFixture(t))
	for _, tc := range []struct{ cause, want error }{
		{persistence.ErrWorkerUnauthorized, ErrWorkerAuthentication},
		{errors.New("open /private/provider-canary/store: backend failure"), ErrWorkerUnavailable},
		{context.Canceled, context.Canceled},
		{context.DeadlineExceeded, context.DeadlineExceeded},
	} {
		reader := workerReaderFunc(func(context.Context, string, time.Time) (persistence.WorkerAuthority, error) {
			return persistence.WorkerAuthority{WorkerID: "must-not-escape"}, tc.cause
		})
		r := requestFixture(http.MethodPost, "/api/v2/external-workers/start", workerToken)
		got, err := a.AuthorizeWorker(r, "WorkerStart", reader)
		if !errors.Is(err, tc.want) || got != (persistence.WorkerAuthority{}) || strings.Contains(err.Error(), "provider-canary") {
			t.Fatal("backend error classification leaked identity or diagnostics", err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	reader := workerReaderFunc(func(context.Context, string, time.Time) (persistence.WorkerAuthority, error) {
		cancel()
		return persistence.WorkerAuthority{WorkerID: "cancelled"}, nil
	})
	r := requestFixture(http.MethodPost, "/api/v2/external-workers/start", workerToken).WithContext(ctx)
	if got, err := a.AuthorizeWorker(r, "WorkerStart", reader); !errors.Is(err, context.Canceled) || got != (persistence.WorkerAuthority{}) {
		t.Fatal("cancellation returned an authority observation", err)
	}
}

func TestWorkerAuthenticationDoesNotHoldManagementLockDuringStoreRead(t *testing.T) {
	a := newFixture(t, configFixture(t))
	entered, release := make(chan struct{}), make(chan struct{})
	reader := workerReaderFunc(func(ctx context.Context, _ string, _ time.Time) (persistence.WorkerAuthority, error) {
		close(entered)
		select {
		case <-release:
			return persistence.WorkerAuthority{}, persistence.ErrWorkerUnauthorized
		case <-ctx.Done():
			return persistence.WorkerAuthority{}, ctx.Err()
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		r := requestFixture(http.MethodPost, "/api/v2/external-workers/start", workerToken).WithContext(ctx)
		_, err := a.AuthorizeWorker(r, "WorkerStart", reader)
		finished <- err
	}()
	<-entered
	replaced := make(chan error, 1)
	config := configFixture(t)
	go func() { replaced <- a.Replace(config) }()
	select {
	case err := <-replaced:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("worker store read blocked management policy replacement")
	}
	close(release)
	if err := <-finished; !errors.Is(err, ErrWorkerAuthentication) {
		t.Fatal(err)
	}
}

func TestWorkerCredentialDeniedByManagementHTTP(t *testing.T) {
	a := newFixture(t, configFixture(t))
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := a.Authorize(r, "GetAccess")
		var typed *Error
		if errors.As(err, &typed) {
			w.WriteHeader(typed.StatusCode())
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	r, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/api/v2/self", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+workerToken)
	response, err := server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatal("worker bearer accessed management HTTP")
	}
}

func TestWorkerAuthenticationCommittedRaftRotation(t *testing.T) {
	cleanupStore := func(s *persistence.Store) {
		t.Cleanup(func() { _ = s.Close() })
	}
	config := runtimeconfig.Default()
	config.Storage.Directory = t.TempDir()
	admin, err := persistence.OpenAdministrative(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	cleanupStore(admin)
	state, err := admin.CommitAuthentication(t.Context(), persistence.AuthenticationCommand{
		Mode: "bootstrap", Epoch: "operator-epoch", Revision: "operator-revision", Actor: "local-bootstrap", At: time.Now().UTC(),
		Principals: []persistence.AuthenticationPrincipal{{ID: "operator-one", Role: "operator", TokenSHA256: verifier(t, operatorToken)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := FromAuthentication(state, nil)
	if err != nil {
		t.Fatal(err)
	}
	operator, err := admin.ObserveOperatorAuthority(t.Context(), "operator-one", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	policy, err := admin.CommitWorkerPolicy(t.Context(), persistence.WorkerPolicyCommand{
		Mode: "bootstrap", Epoch: "worker-epoch", Revision: "worker-revision-1", Actor: operator.Actor, Authority: operator, At: time.Now().UTC(),
		Worker: persistence.WorkerPrincipal{ID: "worker-a", UID: "worker-uid-a", CredentialRevision: "credential-1", GrantRevision: "grants-1", TokenSHA256: verifier(t, workerToken)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := admin.Close(); err != nil {
		t.Fatal(err)
	}
	openOwner := func() *persistence.Store {
		t.Helper()
		s, err := persistence.Open(t.Context(), config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return s
	}
	checkHTTP := func(owner *persistence.Store, token string, status int) persistence.WorkerAuthority {
		t.Helper()
		observed := make(chan persistence.WorkerAuthority, 1)
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authority, err := a.AuthorizeWorker(r, "WorkerResult", owner)
			if err != nil {
				var typed *WorkerError
				if errors.As(err, &typed) {
					w.WriteHeader(typed.StatusCode())
				} else {
					w.WriteHeader(http.StatusInternalServerError)
				}
				return
			}
			observed <- authority
			w.WriteHeader(http.StatusNoContent)
		}))
		defer server.Close()
		r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/api/v2/external-workers/result", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		res, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		_, _ = io.Copy(io.Discard, res.Body)
		if res.StatusCode != status {
			t.Fatalf("committed credential returned HTTP %d; want %d", res.StatusCode, status)
		}
		select {
		case authority := <-observed:
			return authority
		default:
			return persistence.WorkerAuthority{}
		}
	}
	owner := openOwner()
	before := checkHTTP(owner, workerToken, http.StatusNoContent)
	checkHTTP(owner, operatorToken, http.StatusUnauthorized)
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	admin, err = persistence.OpenAdministrative(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	cleanupStore(admin)
	entry := policy.Workers["worker-a"]
	entry.TokenSHA256 = verifier(t, rotatedToken)
	entry.CredentialRevision = "credential-2"
	policy, err = admin.CommitWorkerPolicy(t.Context(), persistence.WorkerPolicyCommand{
		Mode: "upsert", Epoch: policy.Epoch, ExpectedEpoch: policy.Epoch, ExpectedRevision: policy.Revision, Revision: "worker-revision-2", Actor: operator.Actor, Authority: operator, At: time.Now().UTC(), Worker: entry,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := admin.Close(); err != nil {
		t.Fatal(err)
	}
	owner = openOwner()
	checkHTTP(owner, workerToken, http.StatusUnauthorized)
	after := checkHTTP(owner, rotatedToken, http.StatusNoContent)
	if before.WorkerID != after.WorkerID || before.WorkerUID != after.WorkerUID || before.CredentialRevision == after.CredentialRevision || before.GrantRevision != after.GrantRevision || after.PolicyRevision != policy.Revision {
		t.Fatal("rotation did not preserve worker identity independently of credentials")
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	admin, err = persistence.OpenAdministrative(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	cleanupStore(admin)
	entry.Revoked = true
	entry.CredentialRevision = "credential-3"
	if _, err := admin.CommitWorkerPolicy(t.Context(), persistence.WorkerPolicyCommand{
		Mode: "upsert", Epoch: policy.Epoch, ExpectedEpoch: policy.Epoch, ExpectedRevision: policy.Revision, Revision: "worker-revision-3", Actor: operator.Actor, Authority: operator, At: time.Now().UTC(), Worker: entry,
	}); err != nil {
		t.Fatal(err)
	}
	if err := admin.Close(); err != nil {
		t.Fatal(err)
	}
	owner = openOwner()
	checkHTTP(owner, rotatedToken, http.StatusUnauthorized)
}
