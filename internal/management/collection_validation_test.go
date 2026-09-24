package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

var collectionReadAll = CollectionValidationOptions{CanRead: func(persistence.CatalogKey) bool { return true }}

func collectionInputs(resources ...api.Resource) []CollectionInput {
	out := make([]CollectionInput, len(resources))
	for i, r := range resources {
		out[i] = CollectionInput{Resource: r, SourceID: "source-1", ItemID: fmt.Sprintf("item-%d", i)}
	}
	return out
}
func validateCollection(t *testing.T, c *Catalog, inputs []CollectionInput) (CollectionValidation, error) {
	t.Helper()
	view, err := c.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return c.ValidateCollection(context.Background(), view, inputs, collectionReadAll)
}
func collectionMonitor(id, url string) api.Resource {
	return resource("Monitor", id, api.MonitorSpec{Check: api.CheckSpec{Interval: "60s", Timeout: "5s", Driver: api.DriverConfig{Type: "http", Config: json.RawMessage(fmt.Sprintf(`{"url":%q}`, url))}}})
}

type collectionReadOnlyWrapper struct {
	secureconfig.KeyWrapper
	wraps, opens atomic.Int64
}

func (w *collectionReadOnlyWrapper) Wrap(context.Context, []byte, []byte) ([]byte, error) {
	w.wraps.Add(1)
	return nil, errors.New("preflight must not seal")
}
func (w *collectionReadOnlyWrapper) Unwrap(ctx context.Context, key, aad []byte) ([]byte, error) {
	w.opens.Add(1)
	return w.KeyWrapper.Unwrap(ctx, key, aad)
}
func collectionBlockSealing(t *testing.T, c *Catalog) *collectionReadOnlyWrapper {
	t.Helper()
	inner, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
	if err != nil {
		t.Fatal(err)
	}
	wrapper := &collectionReadOnlyWrapper{KeyWrapper: inner}
	c.sealer, err = secureconfig.NewSealer(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	return wrapper
}

func TestCollectionValidationStagedUnionPreservesOmittedCredentialWithoutWrites(t *testing.T) {
	c, store := testCatalog(t)
	secret := "https://example.test/private-provider-hook"
	saved := createResource(t, c, resource("Credential", "hook", api.CredentialSpec{Value: &secret}))
	before, _, _ := store.CatalogGet(persistence.CatalogKey{Kind: "Credential", ID: "hook"})
	index := store.Status().CommittedIndex
	wrapper := collectionBlockSealing(t, c)
	monitor := collectionMonitor("api", "https://example.test/health")
	var spec api.MonitorSpec
	json.Unmarshal(monitor.Spec, &spec)
	spec.Notifications = &map[string]api.AlertRule{"red": {NotifyType: api.Pointer("slack"), GroupRef: api.Pointer("team")}}
	monitor.Spec, _ = json.Marshal(spec)
	credential := resource("Credential", "hook", api.CredentialSpec{})
	credential.Metadata = saved.Metadata
	inputs := collectionInputs(monitor, resource("NotificationGroup", "team", api.NotificationGroupSpec{RecipientRefs: []string{"oncall"}}), resource("Recipient", "oncall", api.RecipientSpec{EndpointRefs: []string{"chat"}}), resource("NotificationEndpoint", "chat", api.DriverConfig{Type: "slack", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"hook": "hook"})}), credential)
	inputBytes, _ := json.Marshal(inputs)
	result, err := validateCollection(t, c, inputs)
	if err != nil || !result.Valid {
		t.Fatalf("staged union: %+v %v", result, err)
	}
	want := []string{"Credential", "NotificationEndpoint", "Recipient", "NotificationGroup", "Monitor"}
	for i, key := range result.Order {
		if key.Kind != want[i] {
			t.Fatal("not dependency ordered", result.Order)
		}
	}
	if result.Items[4].Change != "unchanged" || result.Items[4].ResourceVersion != saved.Metadata.ResourceVersion || len(result.CreateAbsent) != 4 {
		t.Fatal("lost original credential/version or absence guards", result)
	}
	if len(result.Conditions) != 1 || result.Conditions[0].UID != saved.Metadata.UID {
		t.Fatal("live credential not guarded", result.Conditions)
	}
	after, _, _ := store.CatalogGet(before.Key)
	if !reflect.DeepEqual(before, after) || store.Status().CommittedIndex != index || wrapper.wraps.Load() != 0 || wrapper.opens.Load() == 0 {
		t.Fatal("preflight wrote/sealed instead of only reading")
	}
	afterInput, _ := json.Marshal(inputs)
	out, _ := json.Marshal(result)
	if !bytes.Equal(inputBytes, afterInput) || bytes.Contains(out, []byte(secret)) || bytes.Contains(out, []byte("private-provider-hook")) {
		t.Fatal("input mutated or private credential escaped")
	}
}

func TestCollectionValidationRejectsUnsafePrefixWithOutsideConsumer(t *testing.T) {
	c, store := testCatalog(t)
	for _, id := range []string{"a", "b"} {
		createResource(t, c, resource("Credential", id, api.CredentialSpec{Value: api.Pointer("https://example.test/" + id)}))
		createResource(t, c, resource("NotificationEndpoint", id, api.DriverConfig{Type: "slack", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"hook": id})}))
	}
	recipient := createResource(t, c, resource("Recipient", "oncall", api.RecipientSpec{EndpointRefs: []string{"a"}}))
	monitor := collectionMonitor("outside-service", "https://example.test/health")
	var spec api.MonitorSpec
	json.Unmarshal(monitor.Spec, &spec)
	spec.Notifications = &map[string]api.AlertRule{"red": {NotifyType: api.Pointer("slack"), RecipientRefs: api.Pointer([]string{"oncall"})}}
	monitor.Spec, _ = json.Marshal(spec)
	outside := createResource(t, c, monitor)
	nextEndpoint := resource("NotificationEndpoint", "a", api.DriverConfig{Type: "log", Config: json.RawMessage(`{"file":"unopened-collection.log"}`)})
	nextRecipient := resource("Recipient", "oncall", api.RecipientSpec{EndpointRefs: []string{"b"}})
	nextRecipient.Metadata = recipient.Metadata
	index := store.Status().CommittedIndex
	result, err := validateCollection(t, c, collectionInputs(nextRecipient, nextEndpoint))
	if !errors.Is(err, ErrValidation) || result.Valid || result.Items[1].Issue != "unsafePrefix" {
		t.Fatalf("valid final graph must reject broken intermediate endpoint: %+v %v", result, err)
	}
	if !slices.Contains(result.Impacted, persistence.CatalogKey{Kind: "Monitor", ID: outside.Metadata.ID}) {
		t.Fatal("outside monitor impact omitted")
	}
	current, err := c.Get(context.Background(), "Monitor", outside.Metadata.ID)
	if err != nil || current.Metadata.ResourceVersion != outside.Metadata.ResourceVersion || store.Status().CommittedIndex != index {
		t.Fatal("preflight changed outside consumer")
	}
	// The final union is valid when built directly; rejection above is genuinely
	// the old live consumer at the dependency-first prefix, not final validation.
	endpointOnly, err := validateCollection(t, c, collectionInputs(nextEndpoint))
	if err == nil || endpointOnly.Valid {
		t.Fatal("outside-only invalid update unexpectedly passed")
	}
}

func TestCollectionValidationIncludesOutsideGuardsAndRejectsStaleViews(t *testing.T) {
	c, store := testCatalog(t)
	saved := createResource(t, c, resource("Credential", "hook", api.CredentialSpec{Value: api.Pointer("https://example.test/old")}))
	endpoint := createResource(t, c, resource("NotificationEndpoint", "chat", api.DriverConfig{Type: "slack", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"hook": "hook"})}))
	input := resource("Credential", "hook", api.CredentialSpec{Value: api.Pointer("https://example.test/new")})
	input.Metadata = saved.Metadata
	result, err := validateCollection(t, c, collectionInputs(input))
	if err != nil || !result.Valid {
		t.Fatal(result, err)
	}
	if !slices.Contains(result.Impacted, persistence.CatalogKey{Kind: "NotificationEndpoint", ID: "chat"}) || len(result.Conditions) != 2 {
		t.Fatal("outside consumer guards absent", result)
	}
	for _, condition := range result.Conditions {
		if condition.ExpectedDependentsVersion == nil {
			t.Fatal("reverse edge guard omitted")
		}
	}
	view, _ := c.Snapshot()
	next := resource("NotificationEndpoint", "chat", api.DriverConfig{Type: "slack", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"hook": "hook"})})
	next.Metadata = endpoint.Metadata
	next.Metadata.Name = api.Pointer("renamed")
	p, err := c.Prepare(context.Background(), next, endpoint.Metadata.ResourceVersion, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Commit(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	index := store.Status().CommittedIndex
	result, err = c.ValidateCollection(context.Background(), view, collectionInputs(input), collectionReadAll)
	if !errors.Is(err, persistence.ErrCatalogConflict) || result.Valid || store.Status().CommittedIndex != index {
		t.Fatal("stale outside consumer was not rejected", result, err)
	}
}

func TestCollectionValidationReadDenialDoesNotDiscloseOutsideIdentity(t *testing.T) {
	c, _ := testCatalog(t)
	createResource(t, c, resource("Credential", "hook", api.CredentialSpec{Value: api.Pointer("https://example.test/old")}))
	createResource(t, c, resource("NotificationEndpoint", "unseen-team-contact", api.DriverConfig{Type: "slack", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"hook": "hook"})}))
	input := resource("Credential", "hook", api.CredentialSpec{Value: api.Pointer("https://example.test/new")})
	view, _ := c.Snapshot()
	result, err := c.ValidateCollection(context.Background(), view, collectionInputs(input), CollectionValidationOptions{CanRead: func(key persistence.CatalogKey) bool { return key.Kind == "Credential" }})
	raw, _ := json.Marshal(result)
	if err == nil || result.Valid || result.Items[0].Issue != "readDenied" || bytes.Contains(raw, []byte("unseen-team-contact")) || strings.Contains(err.Error(), "unseen-team-contact") {
		t.Fatal("unauthorized dependency identity disclosed", result, err)
	}
	if _, err := c.ValidateCollection(context.Background(), view, collectionInputs(input), CollectionValidationOptions{}); !errors.Is(err, ErrValidation) {
		t.Fatal("nil permission callback accepted")
	}
}

func TestCollectionValidationIndependentMonitorsAreBoundedAndDoNotRunProviders(t *testing.T) {
	c, store := testCatalog(t)
	var calls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer target.Close()
	inputs := make([]CollectionInput, 600)
	for i := range inputs {
		inputs[i] = collectionInputs(collectionMonitor(fmt.Sprintf("monitor-%04d", i), target.URL))[0]
		inputs[i].ItemID = fmt.Sprintf("item-%d", i)
	}
	wrapper := collectionBlockSealing(t, c)
	index := store.Status().CommittedIndex
	result, err := validateCollection(t, c, inputs)
	if err != nil || !result.Valid || len(result.Order) != len(inputs) || len(result.CreateAbsent) != len(inputs) {
		t.Fatal("independent prefix work became quadratic", len(result.Order), err)
	}
	if calls.Load() != 0 || wrapper.wraps.Load() != 0 || store.Status().CommittedIndex != index {
		t.Fatal("pure preflight performed I/O or committed")
	}
	logPath := filepath.Join(t.TempDir(), "must-not-exist.log")
	result, err = validateCollection(t, c, collectionInputs(resource("NotificationEndpoint", "log", api.DriverConfig{Type: "log", Config: json.RawMessage(fmt.Sprintf(`{"file":%q}`, logPath))})))
	if err != nil || !result.Valid {
		t.Fatal(result, err)
	}
	if _, err := os.Stat(logPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("validation created provider log")
	}
}

func TestCollectionValidationRejectsInvalidInputsWithoutChanges(t *testing.T) {
	c, store := testCatalog(t)
	saved := createResource(t, c, resource("Credential", "existing", api.CredentialSpec{Value: api.Pointer("original-provider-secret")}))
	stale := resource("Credential", "existing", api.CredentialSpec{})
	stale.Metadata = saved.Metadata
	stale.Metadata.ResourceVersion = "stale-version"
	nullValue := resource("Credential", "existing", api.CredentialSpec{})
	nullValue.Spec = json.RawMessage(`{"value":null}`)
	unknownDriver := collectionMonitor("missing-build-driver", "https://example.test")
	var spec api.MonitorSpec
	json.Unmarshal(unknownDriver.Spec, &spec)
	spec.Check.Driver = api.DriverConfig{Type: "kafka", Config: json.RawMessage(`{"brokers":["example.test:9092"]}`)}
	unknownDriver.Spec, _ = json.Marshal(spec)
	cases := []struct {
		name   string
		inputs []CollectionInput
		want   error
	}{
		{"malformed final resource", append(collectionInputs(collectionMonitor("good", "https://example.test")), collectionInputs(resource("NotificationGroup", "empty", api.NotificationGroupSpec{}))...), ErrValidation},
		{"duplicate identity", collectionInputs(collectionMonitor("same", "https://example.test"), collectionMonitor("same", "https://example.test")), ErrValidation},
		{"missing staged reference", collectionInputs(resource("Recipient", "contact", api.RecipientSpec{EndpointRefs: []string{"absent"}})), persistence.ErrCatalogDependency},
		{"explicit stale version", collectionInputs(stale), persistence.ErrCatalogConflict},
		{"null credential", collectionInputs(nullValue), ErrValidation},
		{"protected inline secret", collectionInputs(resource("NotificationEndpoint", "private", api.DriverConfig{Type: "slack", Config: json.RawMessage(`{"hook":"https://example.test/do-not-echo"}`)})), ErrValidation},
		{"unsafe source", []CollectionInput{{Resource: collectionMonitor("one", "https://example.test"), SourceID: "https://user:secret@example.test/?token=secret", ItemID: "one"}}, ErrValidation},
	}
	if jobs.ValidateDriver("check", "kafka") != nil {
		cases = append(cases, struct {
			name   string
			inputs []CollectionInput
			want   error
		}{"uncompiled driver", collectionInputs(unknownDriver), ErrValidation})
	}
	index := store.Status().CommittedIndex
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := validateCollection(t, c, tc.inputs)
			if !errors.Is(err, tc.want) || out.Valid || len(out.Order) != 0 {
				t.Fatal(out, err)
			}
			raw, _ := json.Marshal(out)
			if strings.Contains(string(raw), "do-not-echo") || strings.Contains(string(raw), "token=secret") || strings.Contains(err.Error(), "do-not-echo") {
				t.Fatal("unsafe diagnostic")
			}
			if store.Status().CommittedIndex != index {
				t.Fatal("invalid preflight committed")
			}
		})
	}
}

func TestCollectionValidationDeniedReferenceDoesNotRevealExistence(t *testing.T) {
	c, _ := testCatalog(t)
	createResource(t, c, resource("Credential", "secret", api.CredentialSpec{Value: api.Pointer("https://example.test/private")}))
	present, _ := c.Snapshot()
	emptyCatalog, _ := testCatalog(t)
	absent, _ := emptyCatalog.Snapshot()
	input := collectionInputs(resource("NotificationEndpoint", "chat", api.DriverConfig{Type: "slack", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"hook": "secret"})}))
	options := CollectionValidationOptions{CanRead: func(key persistence.CatalogKey) bool { return key.Kind != "Credential" }}
	a, ea := c.ValidateCollection(context.Background(), present, input, options)
	b, eb := emptyCatalog.ValidateCollection(context.Background(), absent, input, options)
	a.Index, b.Index = 0, 0
	if ea == nil || eb == nil || ea.Error() != eb.Error() || !reflect.DeepEqual(a, b) || a.Items[0].Issue != "readDenied" {
		t.Fatal("denied reference existence observable", a, b, ea, eb)
	}
}

func TestCollectionValidationBoundsCancellationAndNoOp(t *testing.T) {
	c, store := testCatalog(t)
	view, _ := c.Snapshot()
	index := store.Status().CommittedIndex
	out, err := c.ValidateCollection(context.Background(), view, nil, collectionReadAll)
	if err != nil || !out.Valid || len(out.Items) != 0 || len(out.Order) != 0 {
		t.Fatal("empty collection is not a no-op", out, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if out, err := c.ValidateCollection(ctx, view, collectionInputs(collectionMonitor("one", "https://example.test")), collectionReadAll); !errors.Is(err, context.Canceled) || out.Valid {
		t.Fatal("canceled validation continued", out, err)
	}
	if _, err := c.ValidateCollection(context.Background(), view, make([]CollectionInput, maxValidationGraph+1), collectionReadAll); !errors.Is(err, ErrGraphLimit) {
		t.Fatal("resource count bound omitted", err)
	}
	oversized := resource("Credential", "one", api.CredentialSpec{Value: api.Pointer(strings.Repeat("s", api.MaxResourceBytes))})
	if out, err := validateCollection(t, c, collectionInputs(oversized)); !errors.Is(err, ErrValidation) || out.Valid {
		t.Fatal("resource byte limit omitted", err)
	}
	payload := strings.Repeat("s", 900<<10)
	inputs := make([]CollectionInput, 38)
	for i := range inputs {
		inputs[i] = CollectionInput{SourceID: "bounded", ItemID: fmt.Sprintf("item-%d", i), Resource: resource("Credential", fmt.Sprintf("credential-%d", i), api.CredentialSpec{Value: &payload})}
	}
	if out, err := validateCollection(t, c, inputs); !errors.Is(err, ErrGraphLimit) || out.Valid {
		t.Fatal("combined byte limit omitted", err)
	}
	// An independently bounded work budget prevents dense prefix validation from
	// continuing forever even when resource bytes fit in the memory accounting.
	validator := collectionValidator{visits: collectionValidationVisits}
	if _, err := validator.validateSet(context.Background(), map[persistence.CatalogKey]api.Resource{{Kind: "Monitor", ID: "one"}: collectionMonitor("one", "https://example.test")}); !errors.Is(err, ErrGraphLimit) {
		t.Fatal("validation work budget omitted", err)
	}
	if store.Status().CommittedIndex != index {
		t.Fatal("limit rejection committed state")
	}
}

func TestCollectionValidationOmittedLiveDependencyVersionIsGuarded(t *testing.T) {
	c, store := testCatalog(t)
	old := createResource(t, c, resource("Credential", "hook", api.CredentialSpec{Value: api.Pointer("https://example.test/original")}))
	view, _ := c.Snapshot()
	updated := resource("Credential", "hook", api.CredentialSpec{Value: api.Pointer("https://example.test/rotated")})
	prepared, err := c.Prepare(context.Background(), updated, old.Metadata.ResourceVersion, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Commit(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	index := store.Status().CommittedIndex
	input := resource("NotificationEndpoint", "new-chat", api.DriverConfig{Type: "slack", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"hook": "hook"})})
	out, err := c.ValidateCollection(context.Background(), view, collectionInputs(input), collectionReadAll)
	if !errors.Is(err, persistence.ErrCatalogConflict) || out.Valid || len(out.Conditions) != 0 || store.Status().CommittedIndex != index {
		t.Fatal("stale live reference was accepted", out, err)
	}
}

func TestCollectionValidationRoutingWorkAndCancellationAreBounded(t *testing.T) {
	// Distinct graph size stays small, while a shared group expands to many
	// endpoint visits across monitors. Actual expansion must consume the budget.
	resources := map[persistence.CatalogKey]api.Resource{}
	refs := make([]string, 200)
	for i := range refs {
		refs[i] = fmt.Sprintf("endpoint-%03d", i)
		key := persistence.CatalogKey{Kind: "NotificationEndpoint", ID: refs[i]}
		resources[key] = resource(key.Kind, key.ID, api.DriverConfig{Type: "log", Config: json.RawMessage(`{"file":"not-opened.log"}`)})
	}
	group := resource("NotificationGroup", "shared", api.NotificationGroupSpec{EndpointRefs: refs})
	resources[persistence.CatalogKey{Kind: group.Kind, ID: group.Metadata.ID}] = group
	for i := 0; i < 600; i++ {
		r := collectionMonitor(fmt.Sprintf("monitor-%03d", i), "https://example.test")
		var spec api.MonitorSpec
		json.Unmarshal(r.Spec, &spec)
		spec.Notifications = &map[string]api.AlertRule{"red": {GroupRef: api.Pointer("shared")}}
		r.Spec, _ = json.Marshal(spec)
		resources[persistence.CatalogKey{Kind: r.Kind, ID: r.Metadata.ID}] = r
	}
	validator := collectionValidator{}
	_, err := validator.validateSet(context.Background(), resources)
	if !errors.Is(err, ErrGraphLimit) || validator.visits != collectionValidationVisits+1 {
		t.Fatal("expanded routing escaped the work bound", validator.visits, err)
	}
	// Cancellation must be honored during one expanded rule, not merely before
	// or after validating an entire monitor or whole catalog.
	catalog := NotificationCatalog{Endpoints: map[string]api.NotificationEndpoint{}, Recipients: map[string]api.Recipient{}, Groups: map[string]api.NotificationGroup{}}
	for _, id := range refs {
		catalog.Endpoints[id] = api.NotificationEndpoint{APIVersion: api.APIVersion, Kind: "NotificationEndpoint", Metadata: api.Metadata{ID: id}, Spec: api.DriverConfig{Type: "log", Config: json.RawMessage(`{"file":"not-opened.log"}`)}}
	}
	catalog.Groups["shared"] = api.NotificationGroup{APIVersion: api.APIVersion, Kind: "NotificationGroup", Metadata: api.Metadata{ID: "shared"}, Spec: api.NotificationGroupSpec{EndpointRefs: refs}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	visits := 0
	_, err = resolveNotificationRule(api.AlertRule{GroupRef: api.Pointer("shared")}, catalog, func() error {
		visits++
		if visits == 20 {
			cancel()
		}
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) || visits != 20 {
		t.Fatal("routing expansion ignored cancellation", visits, err)
	}
}

func TestCollectionValidationFinalMonitorFailureDoesNotRepeatSharedValidation(t *testing.T) {
	resources := map[persistence.CatalogKey]api.Resource{}
	for i := 0; i < 200; i++ {
		id := fmt.Sprintf("endpoint-%03d", i)
		resources[persistence.CatalogKey{Kind: "NotificationEndpoint", ID: id}] = resource("NotificationEndpoint", id, api.DriverConfig{Type: "log", Config: json.RawMessage(`{"file":"not-opened.log"}`)})
	}
	resources[persistence.CatalogKey{Kind: "NotificationGroup", ID: "shared"}] = resource("NotificationGroup", "shared", api.NotificationGroupSpec{EndpointRefs: []string{"endpoint-000"}})
	for i := 0; i < 200; i++ {
		r := collectionMonitor(fmt.Sprintf("monitor-%03d", i), "https://example.test")
		if i == 199 {
			var spec api.MonitorSpec
			json.Unmarshal(r.Spec, &spec)
			spec.Notifications = &map[string]api.AlertRule{"red": {NotifyType: api.Pointer("slack"), GroupRef: api.Pointer("shared")}}
			r.Spec, _ = json.Marshal(spec)
		}
		resources[persistence.CatalogKey{Kind: r.Kind, ID: r.Metadata.ID}] = r
	}
	validator := collectionValidator{}
	key, err := validator.validateSet(context.Background(), resources)
	if !errors.Is(err, ErrValidation) || key.ID != "monitor-199" || validator.visits > 1000 {
		t.Fatal("final invalid monitor repeated shared validation", key, validator.visits, err)
	}
}

func TestCollectionValidationLoadedGuardsDropOwnedEnvelopeCopies(t *testing.T) {
	c, store := testCatalog(t)
	createResource(t, c, resource("Credential", "key", api.CredentialSpec{Value: api.Pointer("private-value")}))
	view, _ := c.Snapshot()
	key := persistence.CatalogKey{Kind: "Credential", ID: "key"}
	before, _, _ := store.CatalogGet(key)
	validator := collectionValidator{catalog: c, view: view.view, options: collectionReadAll, live: map[persistence.CatalogKey]api.Resource{}, records: map[persistence.CatalogKey]persistence.CatalogRecord{}, cause: map[persistence.CatalogKey]int{}}
	loaded, ok, err := validator.load(context.Background(), key, 0)
	defer clear(loaded.Spec)
	if err != nil || !ok {
		t.Fatal(err)
	}
	guard := validator.records[key]
	if !reflect.DeepEqual(guard.Payload, secureconfig.Envelope{}) || len(guard.References) != 0 || guard.UID != before.UID || guard.Revision != before.Revision {
		t.Fatal("loaded guard retained envelope or lost identity")
	}
	after, _, _ := store.CatalogGet(key)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("clearing owned envelope damaged stored ciphertext")
	}
}
