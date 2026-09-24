package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func testCatalog(t *testing.T) (*Catalog, *persistence.Store) {
	t.Helper()
	config := runtimeconfig.Default()
	config.Storage.Mode = "memory"
	store, err := persistence.Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	wrapper, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secureconfig.NewSealer(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := NewCatalog(store, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Ready() {
		t.Fatal("catalog ready before integrity verification")
	}
	if err := catalog.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	return catalog, store
}

func resource(kind, id string, spec any) api.Resource {
	raw, _ := json.Marshal(spec)
	return api.Resource{APIVersion: api.APIVersion, Kind: kind, Metadata: api.Metadata{ID: id}, Spec: raw}
}

func createResource(t *testing.T, c *Catalog, r api.Resource) api.Resource {
	t.Helper()
	p, err := c.Prepare(context.Background(), r, "", true)
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Commit(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if result.CommittedIndex == 0 {
		t.Fatal("missing commit identity")
	}
	return result.Resource
}

func TestCatalogWriteOnlyCredentialAndEncryptedPreparation(t *testing.T) {
	c, store := testCatalog(t)
	secret := "unique-provider-secret-should-never-be-visible"
	credential := createResource(t, c, resource("Credential", "notify-key", api.CredentialSpec{Value: &secret}))
	got, err := c.Get(context.Background(), "Credential", "notify-key")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []api.Resource{credential, got} {
		raw, _ := json.Marshal(r)
		if bytes.Contains(raw, []byte(secret)) || bytes.Contains(raw, []byte(`"value"`)) {
			t.Fatalf("write-only value was returned: %s", raw)
		}
		if !bytes.Contains(raw, []byte(`"available":true`)) {
			t.Fatalf("availability absent: %s", raw)
		}
	}
	view, err := store.CatalogSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	record, ok := view.Get(persistence.CatalogKey{Kind: "Credential", ID: "notify-key"})
	if !ok {
		t.Fatal("not persisted")
	}
	raw, _ := json.Marshal(record)
	if bytes.Contains(raw, []byte(secret)) {
		t.Fatal("plaintext entered durable catalog")
	}
	if err := c.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	patch := []byte(`{"spec":{"description":"shared notifications"}}`)
	p, err := c.PreparePatch(context.Background(), "Credential", "notify-key", got.Metadata.ResourceVersion, patch)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Commit(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	record, _, _ = store.CatalogGet(record.Key)
	stored, err := c.open(context.Background(), record)
	if err != nil || !bytes.Contains(stored.Spec, []byte(secret)) {
		t.Fatal("omitted write-only value was lost", err)
	}
	if record.Generation != 2 {
		t.Fatal("desired metadata within credential spec did not change generation")
	}
}

func TestCatalogCASAndDeleteRecreate(t *testing.T) {
	c, _ := testCatalog(t)
	one := createResource(t, c, resource("NotificationEndpoint", "console", api.DriverConfig{Type: "log", Config: json.RawMessage(`{"file":"/tmp/cpra-not-opened-by-validation.log"}`)}))
	a, err := c.PreparePatch(context.Background(), one.Kind, one.Metadata.ID, one.Metadata.ResourceVersion, []byte(`{"metadata":{"name":"first editor"}}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.PreparePatch(context.Background(), one.Kind, one.Metadata.ID, one.Metadata.ResourceVersion, []byte(`{"metadata":{"name":"second editor"}}`))
	if err != nil {
		t.Fatal(err)
	}
	first, err := c.Commit(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Commit(context.Background(), b); !errors.Is(err, persistence.ErrCatalogConflict) {
		t.Fatalf("second editor overwrote: %v", err)
	}
	if !c.Ready() {
		t.Fatal("expected conflict poisoned readiness")
	}
	if first.Resource.Metadata.Generation != one.Metadata.Generation {
		t.Fatal("name edit changed execution generation")
	}
	del, err := c.PrepareDelete(context.Background(), one.Kind, one.Metadata.ID, first.Resource.Metadata.ResourceVersion)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Commit(context.Background(), del); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Get(context.Background(), one.Kind, one.Metadata.ID); !errors.Is(err, persistence.ErrCatalogNotFound) {
		t.Fatal(err)
	}
	replacement := createResource(t, c, resource("NotificationEndpoint", "console", api.DriverConfig{Type: "log", Config: json.RawMessage(`{"file":"/tmp/cpra-not-opened-by-validation.log"}`)}))
	if replacement.Metadata.UID == one.Metadata.UID {
		t.Fatal("incarnation reused")
	}
	if _, err := c.PrepareDelete(context.Background(), one.Kind, one.Metadata.ID, first.Resource.Metadata.ResourceVersion); !errors.Is(err, persistence.ErrCatalogConflict) {
		t.Fatalf("stale delete reached replacement: %v", err)
	}
}

func TestCatalogRoutingDependenciesAndChangedSharedReferences(t *testing.T) {
	c, store := testCatalog(t)
	secret1, secret2 := "https://example.test/first-token", "https://example.test/second-token"
	oldCredential := createResource(t, c, resource("Credential", "old-key", api.CredentialSpec{Value: &secret1}))
	createResource(t, c, resource("Credential", "new-key", api.CredentialSpec{Value: &secret2}))
	ep := createResource(t, c, resource("NotificationEndpoint", "chat", api.DriverConfig{Type: "slack", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"hook": "old-key"})}))
	createResource(t, c, resource("Recipient", "oncall", api.RecipientSpec{EndpointRefs: []string{"chat"}}))
	createResource(t, c, resource("NotificationGroup", "ops", api.NotificationGroupSpec{RecipientRefs: []string{"oncall"}}))
	monitor := resource("Monitor", "service", api.MonitorSpec{Check: api.CheckSpec{Interval: "60s", Timeout: "5s", Driver: api.DriverConfig{Type: "http", Config: json.RawMessage(`{"url":"https://example.test/health"}`)}}, Notifications: api.Pointer(map[string]api.AlertRule{"red": {NotifyType: api.Pointer("slack"), GroupRef: api.Pointer("ops")}})})
	createResource(t, c, monitor)
	// A shared endpoint edit must validate every monitor through group/contact.
	bad, err := c.PreparePatch(context.Background(), ep.Kind, ep.Metadata.ID, ep.Metadata.ResourceVersion, []byte(`{"spec":{"type":"log","credentialRefs":null}}`))
	if err == nil || bad != nil {
		t.Fatal("endpoint change broke a selected notification type")
	}
	p, err := c.PreparePatch(context.Background(), ep.Kind, ep.Metadata.ID, ep.Metadata.ResourceVersion, []byte(`{"spec":{"credentialRefs":{"hook":"new-key"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Commit(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	// Direct edges ensure an old credential can be removed once the endpoint
	// changed, despite the monitor itself never having been edited.
	del, err := c.PrepareDelete(context.Background(), oldCredential.Kind, oldCredential.Metadata.ID, oldCredential.Metadata.ResourceVersion)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Commit(context.Background(), del); err != nil {
		t.Fatalf("stale transitive references blocked delete: %v", err)
	}
	keyRecord, _, _ := store.CatalogGet(persistence.CatalogKey{Kind: "Credential", ID: "new-key"})
	change, err := c.PreparePatch(context.Background(), "Credential", "new-key", keyRecord.Revision, []byte(`{"spec":{"value":"https://example.test/rotated"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var conditions []string
	for _, condition := range change.mutation.Conditions {
		conditions = append(conditions, condition.Key.Kind+"/"+condition.Key.ID)
	}
	if !strings.Contains(strings.Join(conditions, ","), "Monitor/service") {
		t.Fatalf("future rotation missed monitor via new reference: %v", conditions)
	}
	mon, _ := c.Get(context.Background(), "Monitor", "service")
	concurrent, err := c.PreparePatch(context.Background(), "Monitor", "service", mon.Metadata.ResourceVersion, []byte(`{"metadata":{"labels":{"team":"new-team"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Commit(context.Background(), concurrent); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Commit(context.Background(), change); !errors.Is(err, persistence.ErrCatalogDependency) {
		t.Fatalf("stale affected graph accepted: %v", err)
	}
}

func TestCatalogProtectedValuesAndPatchBoundaries(t *testing.T) {
	c, _ := testCatalog(t)
	for _, driver := range []api.DriverConfig{
		{Type: "slack", Config: json.RawMessage(`{"hook":"do-not-echo-secret"}`)},
		{Type: "webhook", Config: json.RawMessage(`{"headers":{"Authorization":"do-not-echo-secret"}}`)},
		{Type: "slack", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"unknown-do-not-echo-secret": "missing"})},
	} {
		_, err := c.Prepare(context.Background(), resource("NotificationEndpoint", "test", driver), "", true)
		if err == nil || strings.Contains(err.Error(), "do-not-echo-secret") {
			t.Fatalf("unsafe boundary: %v", err)
		}
	}
	secret := "secret"
	r := createResource(t, c, resource("Credential", "key", api.CredentialSpec{Value: &secret}))
	for _, patch := range []string{
		`{"metadata":{"id":"new-id"}}`, `{"status":{"available":false}}`, `{"spec":{"value":null}}`,
		`{"spec":{"value":"[REDACTED]"}}`, `{"spec":{"value":"x","value":"y"}}`, `{} {}`,
	} {
		if _, err := c.PreparePatch(context.Background(), r.Kind, r.Metadata.ID, r.Metadata.ResourceVersion, []byte(patch)); err == nil {
			t.Errorf("accepted forbidden patch %s", patch)
		}
	}
	// Conditions use the observed version even when a caller supplies no body
	// resourceVersion; replacing with omitted write-only value preserves it.
	if _, err := c.Prepare(context.Background(), resource("Credential", "key", api.CredentialSpec{}), "", false); !errors.Is(err, persistence.ErrCatalogConflict) {
		t.Fatal(err)
	}
}

func TestCatalogConsistentPagesAndPreparedChangeOwnership(t *testing.T) {
	c, _ := testCatalog(t)
	for _, id := range []string{"a", "c", "e"} {
		createResource(t, c, resource("NotificationEndpoint", id, api.DriverConfig{Type: "log", Config: json.RawMessage(`{"file":"/tmp/cpra-not-opened-by-validation.log"}`)}))
	}
	view, err := c.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	page, next, err := view.Page(context.Background(), "NotificationEndpoint", "", 2)
	if err != nil || len(page) != 2 || next != "c" {
		t.Fatalf("first page: %v %s %v", page, next, err)
	}
	createResource(t, c, resource("NotificationEndpoint", "b", api.DriverConfig{Type: "log", Config: json.RawMessage(`{"file":"/tmp/cpra-not-opened-by-validation.log"}`)}))
	rest, next, err := view.Page(context.Background(), "NotificationEndpoint", next, 2)
	if err != nil || len(rest) != 1 || rest[0].Metadata.ID != "e" || next != "" {
		t.Fatalf("unstable continuation: %v %s %v", rest, next, err)
	}
	page[0].Spec[0] = 'x'
	got, err := c.Get(context.Background(), "NotificationEndpoint", "a")
	if err != nil || !json.Valid(got.Spec) {
		t.Fatal("read mutated catalog", err)
	}
	p, err := c.PreparePatch(context.Background(), "NotificationEndpoint", "a", got.Metadata.ResourceVersion, []byte(`{"metadata":{"name":"new"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Commit(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Commit(context.Background(), p); !errors.Is(err, ErrValidation) {
		t.Fatal("prepared change replayed", err)
	}
}

func TestCatalogDetectsUnauthenticatedReferenceIndexAndUnknownKinds(t *testing.T) {
	t.Run("changed edge", func(t *testing.T) {
		c, store := testCatalog(t)
		createResource(t, c, resource("NotificationEndpoint", "console", api.DriverConfig{Type: "log", Config: json.RawMessage(`{"file":"/tmp/cpra-not-opened-by-validation.log"}`)}))
		createResource(t, c, resource("Recipient", "person", api.RecipientSpec{EndpointRefs: []string{"console"}}))
		record, _, _ := store.CatalogGet(persistence.CatalogKey{Kind: "Recipient", ID: "person"})
		record.References = nil
		if _, err := c.open(context.Background(), record); !errors.Is(err, ErrUnavailable) {
			t.Fatal("accepted altered cleartext reference index", err)
		}
		if c.Ready() {
			t.Fatal("catalog remained ready after integrity failure")
		}
	})
	t.Run("unrecognized kind", func(t *testing.T) {
		c, store := testCatalog(t)
		now := time.Now().UTC()
		record := persistence.CatalogRecord{Key: persistence.CatalogKey{Kind: "FutureResource", ID: "future"}, UID: "uid", Revision: "revision", Generation: 1, Purpose: "desired-resource", CreatedAt: now, UpdatedAt: now}
		var err error
		record.Payload, err = c.sealer.Seal(context.Background(), record.Binding(c.storeID), []byte(`{"future":"opaque"}`))
		if err != nil {
			t.Fatal(err)
		}
		results, err := store.Submit(context.Background(), []persistence.Command{{Kind: "catalog", At: now, Catalog: &persistence.CatalogMutation{Record: record, Create: true}}})
		if err != nil || results[0].Err != nil {
			t.Fatal("fixture creation", err, results)
		}
		if err := c.Verify(context.Background()); !errors.Is(err, ErrUnavailable) {
			t.Fatal("unknown active resource silently skipped", err)
		}
	})
}
