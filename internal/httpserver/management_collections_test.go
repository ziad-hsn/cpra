package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/fleetview"
	"github.com/ziad-hsn/cpra/internal/httpauth"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	cpra "github.com/ziad-hsn/cpra/sdk/go"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection/commitment"
)

type preflightWrappingCounter struct {
	secureconfig.KeyWrapper
	calls   atomic.Int64
	unwraps atomic.Int64
}

func (w *preflightWrappingCounter) Wrap(ctx context.Context, key, aad []byte) ([]byte, error) {
	w.calls.Add(1)
	return w.KeyWrapper.Wrap(ctx, key, aad)
}

func (w *preflightWrappingCounter) Unwrap(ctx context.Context, wrapped, aad []byte) ([]byte, error) {
	w.unwraps.Add(1)
	return w.KeyWrapper.Unwrap(ctx, wrapped, aad)
}

func newPreflightRaftFixture(t *testing.T) (*managementFixture, *persistence.Store, string, *preflightWrappingCounter) {
	t.Helper()
	directory := t.TempDir()
	cfg := runtimeconfig.Default()
	cfg.Storage.Directory = directory
	store, err := persistence.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	inner, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{19}, 32))
	if err != nil {
		t.Fatal(err)
	}
	wrapper := &preflightWrappingCounter{KeyWrapper: inner}
	sealer, err := secureconfig.NewSealer(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := management.NewCatalog(store, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	operator, _ := httpauth.HashToken(managementOperatorToken)
	reader, _ := httpauth.HashToken(managementReaderToken)
	config := httpauth.Config{Principals: []httpauth.Principal{{ID: "operator", Role: httpauth.Operator, TokenSHA256: operator}, {ID: "reader", Role: httpauth.Reader, TokenSHA256: reader}}}
	auth, err := httpauth.New(config)
	if err != nil {
		t.Fatal(err)
	}
	server := New(ServerConfig{Store: store, Management: catalog, ManagementAuth: auth, Ready: func() bool { return true }}, fleetview.NewHolder(), nil, nil, nil, nil, nil, nil, nil, PublicConfig{})
	mux := http.NewServeMux()
	server.registerAPI(mux)
	transport := httptest.NewTLSServer(server.corsMiddleware(server.authMiddleware(mux)))
	t.Cleanup(transport.Close)
	sdk, err := cpra.New(cpra.Config{BaseURL: transport.URL, AuthToken: managementOperatorToken, HTTPClient: transport.Client()})
	if err != nil {
		t.Fatal(err)
	}
	return &managementFixture{server: server, http: transport, catalog: catalog, authConfig: config, sdk: sdk}, store, directory, wrapper
}

// Inputs here represent separate local source documents. The helper commits to
// those exact source bytes and then to the exact resource bytes sent by the SDK.
func preflightInventory(t *testing.T, resources ...api.Resource) api.PreflightRequest {
	t.Helper()
	key := bytes.Repeat([]byte{0x4a}, commitment.KeyBytes)
	defer clear(key)
	sources, err := commitment.NewSourceAccumulator(key, uint64(len(resources)), commitment.MaxSourceBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer sources.Close()
	raws := make([][]byte, len(resources))
	tokens := make([]string, len(resources))
	for i, r := range resources {
		raws[i], err = json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		tokens[i], _ = commitment.SourceToken(uint64(i + 1))
		if err = sources.Begin(tokens[i]); err != nil {
			t.Fatal(err)
		}
		if _, err = sources.Write(raws[i]); err != nil {
			t.Fatal(err)
		}
		if err = sources.End(); err != nil {
			t.Fatal(err)
		}
	}
	fingerprint, err := sources.Finish()
	if err != nil {
		t.Fatal(err)
	}
	acc, err := commitment.NewAccumulator(key, uint64(len(resources)), fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	defer acc.Close()
	request := api.PreflightRequest{IdentityFormat: commitment.Format, IdentityKey: api.Pointer(hex.EncodeToString(key)), SourceFingerprint: api.Pointer(hex.EncodeToString(fingerprint[:])), ItemCount: int64(len(resources)), Items: make([]api.ApplyItem, len(resources))}
	for i, r := range resources {
		position := commitment.Position{Ordinal: uint64(i + 1), ID: r.Kind + "/" + r.Metadata.ID, Source: commitment.SourcePosition{Token: tokens[i], Document: 1, Item: 1}}
		mac, err := commitment.ItemMAC(key, position, raws[i])
		if err != nil {
			t.Fatal(err)
		}
		if err := acc.Add(position, mac); err != nil {
			t.Fatal(err)
		}
		request.Items[i] = api.ApplyItem{ID: position.ID, Ordinal: int64(position.Ordinal), Source: position.Source.Token, SourceDocument: 1, SourceItem: 1, ContentDigest: hex.EncodeToString(mac[:]), Resource: r}
	}
	digest, err := acc.Finish()
	if err != nil {
		t.Fatal(err)
	}
	request.ContentDigest = hex.EncodeToString(digest[:])
	return request
}
func preflightMonitor(id, target string) api.Resource {
	return managementResource("Monitor", id, api.MonitorSpec{Check: api.CheckSpec{Interval: "60s", Timeout: "5s", Driver: api.DriverConfig{Type: "http", Config: json.RawMessage(fmt.Sprintf(`{"url":%q}`, target))}}})
}
func preflightFiles(t *testing.T, directory string) map[string][32]byte {
	t.Helper()
	files := map[string][32]byte{}
	err := filepath.WalkDir(directory, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(directory, path)
		files[relative] = sha256.Sum256(raw)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestManagementCollectionPreflightSDKUsesStagedReferencesWithoutStateEffects(t *testing.T) {
	f, store, directory, wrapper := newPreflightRaftFixture(t)
	ctx := context.Background()
	var calls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer target.Close()
	secret := "https://example.test/private-hook-for-preflight"
	existing, err := f.sdk.Credentials.Create(ctx, api.Credential{APIVersion: api.APIVersion, Kind: "Credential", Metadata: api.Metadata{ID: "hook"}, Spec: api.CredentialSpec{Value: &secret}})
	if err != nil {
		t.Fatal(err)
	}
	credential := managementResource("Credential", "hook", api.CredentialSpec{})
	credential.Metadata = existing.Data.Metadata
	monitor := preflightMonitor("api", target.URL)
	var spec api.MonitorSpec
	json.Unmarshal(monitor.Spec, &spec)
	spec.Notifications = &map[string]api.AlertRule{"red": {NotifyType: api.Pointer("slack"), GroupRef: api.Pointer("team")}}
	monitor.Spec, _ = json.Marshal(spec)
	request := preflightInventory(t, monitor, managementResource("NotificationGroup", "team", api.NotificationGroupSpec{RecipientRefs: []string{"oncall"}}), managementResource("Recipient", "oncall", api.RecipientSpec{EndpointRefs: []string{"chat"}}), managementResource("NotificationEndpoint", "chat", api.DriverConfig{Type: "slack", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"hook": "hook"})}), credential)
	if err := store.Snapshot(); err != nil {
		t.Fatal(err)
	}
	baseline := preflightFiles(t, directory)
	index := store.Status().CommittedIndex
	wraps := wrapper.calls.Load()
	response, err := f.sdk.Operations.Preflight(ctx, request)
	if err != nil || !response.Data.Valid || len(response.Data.Items) != 5 || response.Data.ContentDigest != request.ContentDigest || string(response.Data.IdentityFormat) != commitment.Format || response.Data.ItemCount == nil || *response.Data.ItemCount != request.ItemCount {
		t.Fatalf("public SDK preflight failed: %v", err)
	}
	if response.OperationID != "" {
		t.Fatal("ephemeral preflight allocated an operation handle")
	}
	for i, item := range response.Data.Items {
		if item.ID != request.Items[i].ID || item.Committed == nil || *item.Committed || item.Applied == nil || *item.Applied {
			t.Fatal("preflight misrepresented committed/application state")
		}
	}
	if response.Data.Items[4].Outcome != "unchanged" || response.Data.Items[4].OldVersion != existing.Data.Metadata.ResourceVersion {
		t.Fatal("omitted credential did not preserve original observation")
	}
	raw, _ := json.Marshal(response.Data)
	if bytes.Contains(raw, []byte(secret)) || bytes.Contains(raw, []byte(*request.IdentityKey)) || bytes.Contains(raw, []byte(*request.SourceFingerprint)) {
		t.Fatal("preflight exposed private input")
	}
	if store.Status().CommittedIndex != index || wrapper.calls.Load() != wraps || calls.Load() != 0 || !reflect.DeepEqual(baseline, preflightFiles(t, directory)) {
		t.Fatal("preflight mutated state files, sealed data, or invoked provider")
	}
	discovery, err := f.sdk.Capabilities(ctx)
	if err != nil || !slices.Contains(discovery.Data.ResourceOperations["Collection"], "PreflightCollection") || !slices.Contains(discovery.Data.ResourceOperations["Operation"], "CreateOperation") || !slices.Contains(discovery.Data.ResourceOperations["Operation"], "ActivateOperation") {
		t.Fatal("preflight discovery overstates executable collection support")
	}
	access, err := f.sdk.Access(ctx)
	if err != nil || !slices.Contains(access.Data.Permissions, "PreflightCollection") || !slices.Contains(access.Data.Permissions, "CreateOperation") || !slices.Contains(access.Data.Permissions, "ActivateOperation") {
		t.Fatal("unsupported operation permission advertised")
	}
}

func TestManagementCollectionPreflightRejectsTamperingAndInvalidEnvelopes(t *testing.T) {
	f := newManagementFixture(t, true)
	base := preflightInventory(t, preflightMonitor("api", "https://example.test/health"))
	raw, _ := json.Marshal(base)
	cases := []struct {
		name   string
		body   []byte
		status int
	}{
		{"resource bytes changed", bytes.Replace(raw, []byte("https://example.test/health"), []byte("https://example.test/changed"), 1), 400},
		{"resource whitespace changed", bytes.Replace(raw, []byte(`"resource":{`), []byte(`"resource":{ `), 1), 400},
		{"duplicate top field", append([]byte(`{"itemCount":1,`), raw[1:]...), 400},
		{"duplicate resource field", bytes.Replace(raw, []byte(`"kind":"Monitor"`), []byte(`"kind":"Monitor","kind":"Monitor"`), 1), 400},
		{"unknown field", append([]byte(`{"private":"do-not-return-this-value",`), raw[1:]...), 400},
		{"case alias", bytes.Replace(raw, []byte(`"identityFormat"`), []byte(`"IdentityFormat"`), 1), 400},
		{"missing key", bytes.Replace(raw, []byte(`"identityKey":"`+*base.IdentityKey+`",`), nil, 1), 400},
		{"key null", bytes.Replace(raw, []byte(`"identityKey":"`+*base.IdentityKey+`"`), []byte(`"identityKey":null`), 1), 400},
		{"uppercase hex", bytes.Replace(raw, []byte(*base.IdentityKey), []byte(strings.ToUpper(*base.IdentityKey)), 1), 400},
		{"count mismatch", bytes.Replace(raw, []byte(`"itemCount":1`), []byte(`"itemCount":0`), 1), 400},
		{"ordinal mismatch", bytes.Replace(raw, []byte(`"ordinal":1`), []byte(`"ordinal":2`), 1), 400},
		{"source path", bytes.Replace(raw, []byte(base.Items[0].Source), []byte("https://user:private@example.test/?key=private"), 1), 400},
		{"unsupported format", bytes.Replace(raw, []byte(commitment.Format), []byte("future-unsupported-format"), 1), 400},
		{"invalid UTF8", append(append([]byte{}, raw...), 0xff), 400},
		{"trailing JSON", append(append([]byte{}, raw...), []byte(` {}`)...), 400},
		{"oversize body", bytes.Repeat([]byte(" "), collectionPreflightBytes+1), 413},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response, body := f.request(t, "POST", "/api/v2/collections/preflight", managementOperatorToken, tc.body, nil)
			if response.StatusCode != tc.status || response.Header.Get("X-Operation-ID") != "" {
				t.Fatalf("unexpected status/handle: %d", response.StatusCode)
			}
			for _, private := range []string{*base.IdentityKey, *base.SourceFingerprint, "do-not-return-this-value", "user:private", "https://example.test/changed"} {
				if bytes.Contains(body, []byte(private)) {
					t.Fatal("input leaked through rejection")
				}
			}
		})
	}
	// Equivalent formatting outside the resource-object span is not part of its
	// item commitment and must not be falsely treated as changed resource bytes.
	spaced := append([]byte(" \n"), raw...)
	response, body := f.request(t, "POST", "/api/v2/collections/preflight", managementOperatorToken, spaced, nil)
	var result api.Preflight
	if response.StatusCode != 200 || api.DecodeResponse(body, &result) != nil || !result.Valid {
		t.Fatal("outer whitespace changed resource identity")
	}
}

func TestManagementCollectionPreflightMalformedFinalResourceHasZeroEffects(t *testing.T) {
	f, store, directory, wrapper := newPreflightRaftFixture(t)
	request := preflightInventory(t, preflightMonitor("first", "https://example.test/health"), managementResource("NotificationGroup", "invalid-final", api.NotificationGroupSpec{}))
	raw, _ := json.Marshal(request)
	if err := store.Snapshot(); err != nil {
		t.Fatal(err)
	}
	files := preflightFiles(t, directory)
	index := store.Status().CommittedIndex
	response, body := f.request(t, "POST", "/api/v2/collections/preflight", managementOperatorToken, raw, nil)
	var result api.Preflight
	if response.StatusCode != 200 || api.DecodeResponse(body, &result) != nil || result.Valid || len(result.Errors) != 1 || result.Errors[0].Field != "items[1].resource" || result.Items[0].Outcome != "notValidated" {
		t.Fatalf("final invalid resource did not reject the whole preflight: %d", response.StatusCode)
	}
	if store.Status().CommittedIndex != index || wrapper.calls.Load() != 0 || !reflect.DeepEqual(files, preflightFiles(t, directory)) {
		t.Fatal("malformed final item caused persistent effects")
	}
	if response.Header.Get("X-Operation-ID") != "" || response.Header.Get("X-Commit-Index") != "" {
		t.Fatal("preflight returned mutation receipt")
	}
}

// Sign altered identity metadata as an independent HTTP caller could. The MAC
// authenticates byte identity; it is not permission to repeat or rename inputs.
func TestManagementCollectionPreflightRejectsCorrectlyCommittedIdentityViolations(t *testing.T) {
	f := newManagementFixture(t, true)
	duplicate := preflightInventory(t, preflightMonitor("same", "https://example.test/one"), preflightMonitor("same", "https://example.test/two"))
	mismatch := preflightInventory(t, preflightMonitor("actual", "https://example.test/one"))
	mismatch.Items[0].ID = "Monitor/different"
	key, _ := hex.DecodeString(*mismatch.IdentityKey)
	defer clear(key)
	position := commitment.Position{Ordinal: 1, ID: mismatch.Items[0].ID, Source: commitment.SourcePosition{Token: mismatch.Items[0].Source, Document: 1, Item: 1}}
	resource, _ := json.Marshal(mismatch.Items[0].Resource)
	mac, err := commitment.ItemMAC(key, position, resource)
	if err != nil {
		t.Fatal(err)
	}
	mismatch.Items[0].ContentDigest = hex.EncodeToString(mac[:])
	fingerprint, err := collectionHex(*mismatch.SourceFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	acc, err := commitment.NewAccumulator(key, 1, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	defer acc.Close()
	if err := acc.Add(position, mac); err != nil {
		t.Fatal(err)
	}
	digest, err := acc.Finish()
	if err != nil {
		t.Fatal(err)
	}
	mismatch.ContentDigest = hex.EncodeToString(digest[:])
	for _, tc := range []struct {
		name    string
		request api.PreflightRequest
		reason  string
	}{{"repeated ID at distinct ordinals", duplicate, "invalidCollection"}, {"inventory ID differs from resource", mismatch, "identityMismatch"}} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(tc.request)
			response, body := f.request(t, "POST", "/api/v2/collections/preflight", managementOperatorToken, raw, nil)
			var problem api.Problem
			if response.StatusCode != 400 || json.Unmarshal(body, &problem) != nil || problem.Code != tc.reason || response.Header.Get("X-Operation-ID") != "" {
				t.Fatalf("correctly signed identity violation was not rejected: %d", response.StatusCode)
			}
		})
	}
}

func TestManagementCollectionPreflightMissingReferenceIsAnUncommittedValidationResult(t *testing.T) {
	f := newManagementFixture(t, true)
	request := preflightInventory(t, managementResource("Recipient", "person", api.RecipientSpec{EndpointRefs: []string{"absent"}}))
	response, err := f.sdk.Operations.Preflight(context.Background(), request)
	if err != nil || response.Data.Valid || response.OperationID != "" || response.Data.ItemCount == nil || *response.Data.ItemCount != 1 || len(response.Data.Items) != 1 || len(response.Data.Errors) != 1 || response.Data.Errors[0].Field != "items[0].resource" || response.Data.Errors[0].Reason != "missingReference" {
		t.Fatal("graph rejection was not returned as a bounded invalid preflight", err)
	}
	item := response.Data.Items[0]
	if item.Committed == nil || *item.Committed || item.Applied == nil || *item.Applied {
		t.Fatal("invalid preflight did not explicitly report zero mutation")
	}
}

func TestManagementCollectionPreflightResourceAndItemBounds(t *testing.T) {
	f := newManagementFixture(t, true)
	base := preflightInventory(t, preflightMonitor("api", "https://example.test/health"))
	raw, _ := json.Marshal(base)
	var wire collectionPreflightWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	var item collectionPreflightItem
	if err := json.Unmarshal(wire.Items[0], &item); err != nil {
		t.Fatal(err)
	}
	// The per-resource bound precedes MAC work. Keep the entire HTTP request
	// below 4 MiB so a generic body limit cannot hide a missing resource bound.
	item.Resource = append([]byte(`{"oversized":"`), bytes.Repeat([]byte("x"), commitment.MaxResourceBytes)...)
	item.Resource = append(item.Resource, '"', '}')
	wire.Items[0], _ = json.Marshal(item)
	oversized, _ := json.Marshal(wire)
	if len(oversized) >= collectionPreflightBytes {
		t.Fatal("test request exceeds the outer byte bound")
	}
	// Null entries keep the request small. Count validation must reject the
	// collection before inspecting the individual entries.
	wire.Items = make([]json.RawMessage, collectionPreflightItems+1)
	wire.ItemCount = int64(len(wire.Items))
	tooMany, _ := json.Marshal(wire)
	for _, tc := range []struct {
		name   string
		body   []byte
		status int
		code   string
	}{{"resource bytes", oversized, 413, "resourceTooLarge"}, {"item count", tooMany, 400, "invalidCollection"}} {
		t.Run(tc.name, func(t *testing.T) {
			response, body := f.request(t, "POST", "/api/v2/collections/preflight", managementOperatorToken, tc.body, nil)
			var problem api.Problem
			if response.StatusCode != tc.status || json.Unmarshal(body, &problem) != nil || problem.Code != tc.code || response.Header.Get("X-Operation-ID") != "" {
				t.Fatalf("wrong bounded rejection: %d", response.StatusCode)
			}
		})
	}
}

func TestManagementCollectionPreflightPermissionDenialPrecedesDecryption(t *testing.T) {
	f, _, _, wrapper := newPreflightRaftFixture(t)
	secret := "preflight-private-existing-credential"
	_, err := f.sdk.Credentials.Create(context.Background(), api.Credential{APIVersion: api.APIVersion, Kind: "Credential", Metadata: api.Metadata{ID: "present"}, Spec: api.CredentialSpec{Value: &secret}})
	if err != nil {
		t.Fatal(err)
	}
	// Current server roles grant all writes or read-only access. Exercise this
	// internal boundary with scoped permissions so a later finer-grained policy
	// cannot start decrypting a denied dependency or reveal whether it exists.
	access := api.AccessInfo{Permissions: []string{"GetNotificationEndpoint", "CreateNotificationEndpoint", "ReplaceNotificationEndpoint"}}
	unwraps := wrapper.unwraps.Load()
	var previous []byte
	for _, reference := range []string{"present", "absent"} {
		request := preflightInventory(t, managementResource("NotificationEndpoint", "chat", api.DriverConfig{Type: "slack", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"hook": reference})}))
		raw, _ := json.Marshal(request)
		wire, items, err := verifyCollectionPreflight(context.Background(), raw)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.server.managementHTTP.validateCollectionPreflight(context.Background(), wire, items, access)
		if err == nil {
			t.Fatal("denied dependency was validated")
		}
		response := httptest.NewRecorder()
		writeManagementError(response, err)
		if response.Code != 403 || wrapper.unwraps.Load() != unwraps || bytes.Contains(response.Body.Bytes(), []byte(secret)) || previous != nil && !bytes.Equal(previous, response.Body.Bytes()) {
			t.Fatal("denied dependency was decrypted or exposed existence")
		}
		previous = bytes.Clone(response.Body.Bytes())
	}
}

type preflightBodyCounter struct{ reads int }

func (b *preflightBodyCounter) Read([]byte) (int, error) { b.reads++; return 0, io.EOF }

func TestManagementCollectionPreflightAuthorizationDoesNotReadBody(t *testing.T) {
	f := newManagementFixture(t, true)
	for _, token := range []string{"", managementReaderToken} {
		body := &preflightBodyCounter{}
		request := httptest.NewRequest("POST", "https://example.test/api/v2/collections/preflight", body)
		request.Header.Set("Content-Type", "application/json")
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		f.server.managementHTTP.handleCollectionPreflight(response, request)
		if body.reads != 0 || response.Code != 401 && response.Code != 403 {
			t.Fatal("unauthorized preflight consumed its request body")
		}
	}
}

func TestManagementCollectionPreflightAuthorizationQuotaAndEmpty(t *testing.T) {
	f := newManagementFixture(t, true)
	request := preflightInventory(t)
	raw, _ := json.Marshal(request)
	for _, tc := range []struct {
		token   string
		headers map[string]string
		status  int
	}{
		{"", nil, 401}, {managementReaderToken, nil, 403}, {managementLegacyToken, nil, 403}, {managementOperatorToken, map[string]string{"Origin": "https://foreign.invalid"}, 403}, {managementOperatorToken, map[string]string{"Content-Encoding": "gzip"}, 415},
	} {
		response, _ := f.request(t, "POST", "/api/v2/collections/preflight", tc.token, raw, tc.headers)
		if response.StatusCode != tc.status {
			t.Fatal("authorization or encoding gate failed", response.StatusCode)
		}
	}
	response, err := f.sdk.Operations.Preflight(context.Background(), request)
	if err != nil || !response.Data.Valid || len(response.Data.Items) != 0 || response.OperationID != "" {
		t.Fatal("empty inventory did not produce an explicit no-op", err)
	}
	m := f.server.managementHTTP
	for i := 0; i < cap(m.mutations); i++ {
		m.mutations <- struct{}{}
	}
	httpResponse, _ := f.request(t, "POST", "/api/v2/collections/preflight", managementOperatorToken, raw, nil)
	for i := 0; i < cap(m.mutations); i++ {
		<-m.mutations
	}
	if httpResponse.StatusCode != 429 || httpResponse.Header.Get("Retry-After") != "1" {
		t.Fatal("preflight bypassed shared validation capacity")
	}
	// A deliberately stopped mutation admission does not turn this pure read
	// into an operation; it can still validate retained configuration.
	if err := f.server.StopAdmission(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.sdk.Operations.Preflight(context.Background(), request); err != nil {
		t.Fatal("pure preflight incorrectly required mutation admission", err)
	}
}

func TestManagementCollectionPreflightSlowBodyIsActuallyBounded(t *testing.T) {
	f := newManagementFixture(t, true)
	short := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 80*time.Millisecond)
		defer cancel()
		f.http.Config.Handler.ServeHTTP(w, r.WithContext(ctx))
	}))
	defer short.Close()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	request, err := http.NewRequest("POST", short.URL+"/api/v2/collections/preflight", reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+managementOperatorToken)
	request.Header.Set("Content-Type", "application/json")
	sent := make(chan struct{})
	go func() { _, _ = writer.Write([]byte("{")); close(sent) }()
	started := time.Now()
	response, err := short.Client().Do(request)
	writer.Close()
	<-sent
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if time.Since(started) > time.Second || response.StatusCode == 200 {
		t.Fatal("context did not bound the actual body read")
	}
}
