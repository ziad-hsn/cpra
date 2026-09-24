package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func stagedValidationFixture(t *testing.T, resources ...api.Resource) collectionSourceFixture {
	t.Helper()
	return stagedSourceFixture(t, len(resources), func(i int) (persistence.CatalogKey, []byte) {
		r := resources[i]
		raw, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		return persistence.CatalogKey{Kind: r.Kind, ID: r.Metadata.ID}, raw
	}, nil, nil)
}

func stagedValidationRun(t *testing.T, f *collectionSourceFixture, options CollectionValidationOptions) (CollectionValidation, error, *collectionValidationSource) {
	t.Helper()
	source := f.open(t, options.CanRead)
	captured, err := f.catalog.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	result, err := f.catalog.validateStagedCollection(context.Background(), captured, source, options)
	if source.liveBytes != 0 {
		t.Fatal("validation retained plaintext reservation", source.liveBytes)
	}
	return result, err, source
}

func stagedValidationParity(t *testing.T, f *collectionSourceFixture, resources []api.Resource) (CollectionValidation, error) {
	t.Helper()
	want, wantErr := validateCollection(t, f.catalog, collectionInputs(resources...))
	got, gotErr, _ := stagedValidationRun(t, f, collectionReadAll)
	if !errors.Is(gotErr, wantErr) {
		t.Fatalf("staged/request-local error differs: %v / %v", gotErr, wantErr)
	}
	for i := range want.Items {
		want.Items[i].SourceID, want.Items[i].ItemID = got.Items[i].SourceID, got.Items[i].ItemID
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("staged metadata/outcome differs\nrequest-local: %+v\nstaged: %+v", want, got)
	}
	return got, gotErr
}

func TestStagedValidationParityOmittedCredentialAndNoEffects(t *testing.T) {
	monitor := collectionMonitor("api", "https://example.test/health")
	var spec api.MonitorSpec
	_ = json.Unmarshal(monitor.Spec, &spec)
	spec.Notifications = &map[string]api.AlertRule{"red": {NotifyType: api.Pointer("slack"), GroupRef: api.Pointer("team")}}
	monitor.Spec, _ = json.Marshal(spec)
	inputs := []api.Resource{monitor,
		resource("NotificationGroup", "team", api.NotificationGroupSpec{RecipientRefs: []string{"oncall"}}),
		resource("Recipient", "oncall", api.RecipientSpec{EndpointRefs: []string{"chat"}}),
		resource("NotificationEndpoint", "chat", api.DriverConfig{Type: "slack", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"hook": "hook"})}),
		resource("Credential", "hook", api.CredentialSpec{})}
	f := stagedValidationFixture(t, inputs...)
	secret := "https://example.test/private-retained-hook"
	saved := createResource(t, f.catalog, resource("Credential", "hook", api.CredentialSpec{Value: &secret}))
	index := f.store.Status().CommittedIndex
	before, _, _ := f.store.CollectionGet(f.head.ID)
	wrapper := collectionBlockSealing(t, f.catalog)
	result, err := stagedValidationParity(t, &f, inputs)
	if err != nil || !result.Valid || result.Items[4].Change != "unchanged" || result.Items[4].ResourceVersion != saved.Metadata.ResourceVersion {
		t.Fatal("omitted credential changed its original identity", result, err)
	}
	encoded, _ := json.Marshal(result)
	if bytes.Contains(encoded, []byte(secret)) || bytes.Contains(encoded, []byte("private-retained-hook")) {
		t.Fatal("secret entered result metadata")
	}
	after, _, _ := f.store.CollectionGet(f.head.ID)
	if !reflect.DeepEqual(before, after) || f.store.Status().CommittedIndex != index || wrapper.wraps.Load() != 0 {
		t.Fatal("validation renewed staging, sealed or committed input")
	}
	// Original omitted bytes and their MAC remain intact after normalization.
	source := f.open(t, collectionReadAll.CanRead)
	if _, err := source.withResource(context.Background(), persistence.CatalogKey{Kind: "Credential", ID: "hook"}, func(r *api.Resource) error {
		if bytes.Contains(r.Spec, []byte("value")) {
			t.Fatal("preserved credential was written into original input")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStagedValidationParityUnsafePrefixAndOutsideGuards(t *testing.T) {
	inputs := []api.Resource{
		resource("Recipient", "oncall", api.RecipientSpec{EndpointRefs: []string{"b"}}),
		resource("NotificationEndpoint", "a", api.DriverConfig{Type: "log", Config: json.RawMessage(`{"file":"never-opened-staged.log"}`)}),
	}
	f := stagedValidationFixture(t, inputs...)
	for _, id := range []string{"a", "b"} {
		createResource(t, f.catalog, resource("Credential", id, api.CredentialSpec{Value: api.Pointer("https://example.test/" + id)}))
		createResource(t, f.catalog, resource("NotificationEndpoint", id, api.DriverConfig{Type: "slack", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"hook": id})}))
	}
	createResource(t, f.catalog, resource("Recipient", "oncall", api.RecipientSpec{EndpointRefs: []string{"a"}}))
	monitor := collectionMonitor("outside", "https://example.test/health")
	var spec api.MonitorSpec
	_ = json.Unmarshal(monitor.Spec, &spec)
	spec.Notifications = &map[string]api.AlertRule{"red": {NotifyType: api.Pointer("slack"), RecipientRefs: api.Pointer([]string{"oncall"})}}
	monitor.Spec, _ = json.Marshal(spec)
	createResource(t, f.catalog, monitor)
	index := f.store.Status().CommittedIndex
	result, err := stagedValidationParity(t, &f, inputs)
	if !errors.Is(err, ErrValidation) || result.Valid || result.Items[1].Issue != "unsafePrefix" ||
		!slices.Contains(result.Impacted, persistence.CatalogKey{Kind: "Monitor", ID: "outside"}) || f.store.Status().CommittedIndex != index {
		t.Fatal("unsafe old consumer prefix was accepted", result, err)
	}
}

func TestStagedValidationGuardsOutsideEditsAndReverseEdges(t *testing.T) {
	for _, mode := range []string{"stable", "old-version", "new-dependent", "new-target"} {
		t.Run(mode, func(t *testing.T) {
			input := resource("Credential", "hook", api.CredentialSpec{Value: api.Pointer("https://example.test/new")})
			f := stagedValidationFixture(t, input)
			if mode != "new-target" {
				createResource(t, f.catalog, resource("Credential", "hook", api.CredentialSpec{Value: api.Pointer("https://example.test/old")}))
			}
			var endpoint api.Resource
			if mode == "stable" || mode == "old-version" {
				endpoint = createResource(t, f.catalog, resource("NotificationEndpoint", "chat", api.DriverConfig{Type: "slack", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"hook": "hook"})}))
			}
			captured, _ := f.catalog.Snapshot()
			source := f.open(t, nil)
			switch mode {
			case "old-version":
				next := resource("NotificationEndpoint", "chat", api.DriverConfig{Type: "slack", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"hook": "hook"})})
				next.Metadata = endpoint.Metadata
				next.Metadata.Name = api.Pointer("edited")
				prepared, err := f.catalog.Prepare(context.Background(), next, endpoint.Metadata.ResourceVersion, false)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.catalog.Commit(context.Background(), prepared); err != nil {
					t.Fatal(err)
				}
			case "new-dependent":
				createResource(t, f.catalog, resource("NotificationEndpoint", "new-chat", api.DriverConfig{Type: "slack", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"hook": "hook"})}))
			case "new-target":
				createResource(t, f.catalog, input)
			}
			index := f.store.Status().CommittedIndex
			result, err := f.catalog.validateStagedCollection(context.Background(), captured, source, collectionReadAll)
			if mode == "stable" {
				if err != nil || !result.Valid || len(result.Conditions) != 2 || len(result.Impacted) != 1 {
					t.Fatal("outside guard omitted", result, err)
				}
				for _, guard := range result.Conditions {
					if guard.ExpectedDependentsVersion == nil {
						t.Fatal("missing reverse-edge version")
					}
				}
			} else if !errors.Is(err, persistence.ErrCatalogConflict) || result.Valid {
				t.Fatal("stale captured graph accepted", result, err)
			}
			if source.liveBytes != 0 || f.store.Status().CommittedIndex != index {
				t.Fatal("validation retained buffers or committed state")
			}
		})
	}
}

func TestStagedValidationDeniedIdentitiesNeverDiscloseExistence(t *testing.T) {
	for _, exists := range []bool{false, true} {
		t.Run(fmt.Sprintf("exists=%t", exists), func(t *testing.T) {
			f := stagedValidationFixture(t, resource("NotificationEndpoint", "chat", api.DriverConfig{Type: "slack", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"hook": "hidden"})}))
			if exists {
				createResource(t, f.catalog, resource("Credential", "hidden", api.CredentialSpec{Value: api.Pointer("https://example.test/private-value")}))
			}
			options := CollectionValidationOptions{CanRead: func(key persistence.CatalogKey) bool { return key.Kind != "Credential" }}
			wrapper := collectionBlockSealing(t, f.catalog)
			result, err, _ := stagedValidationRun(t, &f, options)
			if !errors.Is(err, errCollectionReadDenied) || result.Valid || result.Items[0].Issue != "readDenied" || len(result.Conditions) != 0 {
				t.Fatal("denied identity changed observable outcome", result, err)
			}
			if wrapper.opens.Load() != 2 {
				t.Fatal("denied reference caused a key unwrap; expected only header and staged endpoint", wrapper.opens.Load())
			}
		})
	}
	input := resource("Credential", "hook", api.CredentialSpec{Value: api.Pointer("https://example.test/new")})
	f := stagedValidationFixture(t, input)
	createResource(t, f.catalog, resource("Credential", "hook", api.CredentialSpec{Value: api.Pointer("https://example.test/old")}))
	createResource(t, f.catalog, resource("NotificationEndpoint", "private-outside-identity", api.DriverConfig{Type: "slack", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"hook": "hook"})}))
	result, err, _ := stagedValidationRun(t, &f, CollectionValidationOptions{CanRead: func(key persistence.CatalogKey) bool { return key.Kind == "Credential" }})
	raw, _ := json.Marshal(result)
	if !errors.Is(err, errCollectionReadDenied) || bytes.Contains(raw, []byte("private-outside-identity")) || strings.Contains(err.Error(), "private-outside-identity") {
		t.Fatal("outside denial disclosed identity", err)
	}
}

func TestStagedValidationRejectsLateMalformedOrInventoryMismatchWithoutEffects(t *testing.T) {
	for _, mode := range []string{"last-schema", "inventory"} {
		t.Run(mode, func(t *testing.T) {
			input := sourceCredentials(10)
			f := stagedSourceFixture(t, 3, func(i int) (persistence.CatalogKey, []byte) {
				key, raw := input(i)
				if mode == "last-schema" && i == 2 {
					clear(raw)
					raw = []byte(`{"kind":`)
				}
				return key, raw
			}, func(head *persistence.CollectionState) {
				if mode == "inventory" {
					head.ContentDigest = strings.Repeat("a", 64)
				}
			}, nil)
			index := f.store.Status().CommittedIndex
			result, err, _ := stagedValidationRun(t, &f, collectionReadAll)
			if !errors.Is(err, ErrValidation) || result.Valid || f.store.Status().CommittedIndex != index {
				t.Fatal("invalid complete inventory succeeded or wrote", result, err)
			}
			for i, item := range result.Items {
				if mode == "last-schema" && i == 2 {
					if item.Issue == "" || item.ItemID != "item.00000000000000000003" {
						t.Fatal("late error lost original ordinal", result)
					}
				} else if item.Issue != "" {
					t.Fatal("global/late failure blamed an earlier valid item", result)
				}
			}
		})
	}
}

func TestStagedValidationIndependentMonitorsNeverInvokeTargets(t *testing.T) {
	var calls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer target.Close()
	resources := make([]api.Resource, 100)
	for i := range resources {
		resources[i] = collectionMonitor(fmt.Sprintf("monitor-%04d", i), target.URL)
	}
	f := stagedValidationFixture(t, resources...)
	index := f.store.Status().CommittedIndex
	wrapper := collectionBlockSealing(t, f.catalog)
	result, err := stagedValidationParity(t, &f, resources)
	if err != nil || !result.Valid || len(result.Order) != 100 || calls.Load() != 0 || wrapper.wraps.Load() != 0 || f.store.Status().CommittedIndex != index {
		t.Fatal("pure validation invoked or changed runtime state", result.Valid, err)
	}
}

func TestStagedValidationLargeInventoryAndNearLimitUpdates(t *testing.T) {
	t.Run("aggregate exceeds 64 MiB", func(t *testing.T) {
		const count, size = 72, 950 << 10
		f := stagedSourceFixture(t, count, sourceCredentials(size), nil, nil)
		result, err, source := stagedValidationRun(t, &f, collectionReadAll)
		if err != nil || !result.Valid || len(result.Order) != count || source.peakBytes > collectionSourcePlaintextBudget {
			t.Fatal("aggregate bytes accumulated as retained plaintext", len(result.Order), source.peakBytes, err)
		}
		t.Logf("validated >%d total plaintext bytes, peak accounted borrow/scratch %d bytes; this is not RSS", count*size, source.peakBytes)
	})
	for _, mode := range []string{"credential update", "omitted credential", "endpoint update"} {
		t.Run(mode, func(t *testing.T) {
			large := strings.Repeat("x", (1<<20)-2048)
			old := resource("Credential", "large", api.CredentialSpec{Value: &large})
			input := resource("Credential", "large", api.CredentialSpec{Value: api.Pointer("y" + large[1:])})
			if mode == "omitted credential" {
				input = resource("Credential", "large", api.CredentialSpec{})
			}
			if mode == "endpoint update" {
				config, _ := json.Marshal(map[string]string{"file": "/tmp/" + large})
				old = resource("NotificationEndpoint", "large", api.DriverConfig{Type: "log", Config: config})
				input = old
				input.Metadata.Name = api.Pointer("new name")
			}
			f := stagedValidationFixture(t, input)
			createResource(t, f.catalog, old)
			result, err, source := stagedValidationRun(t, &f, collectionReadAll)
			if err != nil || !result.Valid || source.peakBytes > collectionSourcePlaintextBudget {
				t.Fatal("near-limit supported resource rejected by redundant reservations", result, source.peakBytes, err)
			}
			if mode == "omitted credential" && result.Items[0].Change != "unchanged" {
				t.Fatal("omission changed value")
			}
			t.Logf("peak accounted borrow/scratch %d bytes", source.peakBytes)
		})
	}
}

func TestStagedValidationInvalidAndNoopParity(t *testing.T) {
	for _, mode := range []string{"noop", "missing reference", "version conflict", "credential null", "redaction", "driver capability"} {
		t.Run(mode, func(t *testing.T) {
			old := resource("Credential", "secret", api.CredentialSpec{Value: api.Pointer("private-value")})
			input := old
			switch mode {
			case "missing reference":
				input = resource("Recipient", "oncall", api.RecipientSpec{EndpointRefs: []string{"missing"}})
			case "version conflict":
				input.Metadata.ResourceVersion = "stale-version"
			case "credential null":
				input.Spec = json.RawMessage(`{"value":null}`)
			case "redaction":
				input = resource("Credential", "secret", api.CredentialSpec{Value: api.Pointer("[REDACTED]")})
			case "driver capability":
				input = collectionMonitor("unsupported", "https://example.test/health")
				var spec api.MonitorSpec
				_ = json.Unmarshal(input.Spec, &spec)
				spec.Check.Driver = api.DriverConfig{Type: "mysql", Config: json.RawMessage(`{"host":"localhost","port":3306}`)}
				input.Spec, _ = json.Marshal(spec)
			}
			f := stagedValidationFixture(t, input)
			if mode != "missing reference" && mode != "driver capability" {
				createResource(t, f.catalog, old)
			}
			result, err := stagedValidationParity(t, &f, []api.Resource{input})
			if mode == "noop" && (err != nil || !result.Valid || result.Items[0].Change != "unchanged") {
				t.Fatal("unchanged input was not recognized", err)
			}
		})
	}
}

func TestStagedValidationCancellationExpiryAndQuotaRelease(t *testing.T) {
	inputs := make([]api.Resource, 20)
	for i := range inputs {
		inputs[i] = collectionMonitor(fmt.Sprintf("monitor-%03d", i), "https://example.test/health")
	}
	for _, mode := range []string{"cancel-early", "cancel-graph", "expire-graph", "scratch-limit"} {
		t.Run(mode, func(t *testing.T) {
			f := stagedValidationFixture(t, inputs...)
			source := f.open(t, nil)
			captured, _ := f.catalog.Snapshot()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			source.now = func() time.Time {
				calls++
				if mode == "cancel-early" && calls >= 2 || mode == "cancel-graph" && calls >= 250 {
					cancel()
				}
				if mode == "expire-graph" && calls >= 250 {
					return f.head.ExpiresAt
				}
				return f.at
			}
			var release func()
			if mode == "scratch-limit" {
				var err error
				release, err = source.reserve(collectionSourcePlaintextBudget)
				if err != nil {
					t.Fatal(err)
				}
			}
			index := f.store.Status().CommittedIndex
			result, err := f.catalog.validateStagedCollection(ctx, captured, source, collectionReadAll)
			if release != nil {
				release()
			}
			want := error(context.Canceled)
			if mode == "expire-graph" {
				want = persistence.ErrOperationExpired
			} else if mode == "scratch-limit" {
				want = ErrGraphLimit
			}
			if !errors.Is(err, want) || result.Valid || source.liveBytes != 0 || f.store.Status().CommittedIndex != index {
				t.Fatal("interruption/limit escaped or leaked", mode, calls, result.Valid, source.liveBytes, err)
			}
		})
	}
}

func TestStagedValidationLiveReadsFenceExpiryBeforeUnwrap(t *testing.T) {
	f := stagedValidationFixture(t, resource("Credential", "staged", api.CredentialSpec{Value: api.Pointer("staged-value")}))
	createResource(t, f.catalog, resource("Credential", "live", api.CredentialSpec{Value: api.Pointer("private-live-value")}))
	captured, _ := f.catalog.Snapshot()
	source := f.open(t, nil)
	wrapper := collectionBlockSealing(t, f.catalog)
	v := stagedValidator{catalog: f.catalog, captured: captured, source: source, options: collectionReadAll,
		live: map[persistence.CatalogKey]stagedValidationVersion{}}
	f.at = f.head.ExpiresAt
	for _, id := range []string{"live", "absent"} {
		if _, err := v.withLive(context.Background(), persistence.CatalogKey{Kind: "Credential", ID: id}, func(*api.Resource) error {
			t.Fatal("expired live-resource callback")
			return nil
		}); !errors.Is(err, persistence.ErrOperationExpired) {
			t.Fatal("expired lookup proceeded", err)
		}
	}
	if wrapper.opens.Load() != 0 || source.liveBytes != 0 {
		t.Fatal("expired lookup unwrapped ciphertext or reserved plaintext")
	}
	f.at = f.head.ActivityAt
	if _, err := v.withLive(context.Background(), persistence.CatalogKey{Kind: "Credential", ID: "live"}, func(*api.Resource) error {
		f.at = f.head.ExpiresAt
		return nil
	}); !errors.Is(err, persistence.ErrOperationExpired) || source.liveBytes != 0 {
		t.Fatal("expiry during live borrow was ignored", err)
	}
}

func TestStagedValidationMetadataAndWorkBoundsBeforeGrowth(t *testing.T) {
	key := persistence.CatalogKey{Kind: "Credential", ID: "limited"}
	version := stagedValidationVersion{refs: []persistence.CatalogKey{{Kind: "Credential", ID: strings.Repeat("x", 256)}}}
	for _, v := range []*stagedValidator{{bytes: stagedValidationMetadataBytes - 1024}, {versions: maxValidationGraph}} {
		beforeBytes, beforeVersions := v.bytes, v.versions
		if err := v.retain(key, version, 0); !errors.Is(err, ErrGraphLimit) || v.bytes != beforeBytes || v.versions != beforeVersions {
			t.Fatal("metadata quota mutated before admission", err)
		}
	}
	v := stagedValidator{visits: collectionValidationVisits, desired: map[persistence.CatalogKey]stagedValidationVersion{key: {}}}
	if _, err := v.order(context.Background()); !errors.Is(err, ErrGraphLimit) {
		t.Fatal("traversal work bound ignored", err)
	}
}

func TestStagedValidationLargeNotificationClosureUsesLookups(t *testing.T) {
	const endpoints, valueBytes = 40, 900 << 10
	refs := make([]string, endpoints)
	for i := range refs {
		refs[i] = fmt.Sprintf("endpoint-%03d", i)
	}
	f := stagedSourceFixture(t, endpoints+2, func(i int) (persistence.CatalogKey, []byte) {
		var r api.Resource
		switch {
		case i < endpoints:
			config, _ := json.Marshal(map[string]string{"file": "/tmp/never-opened-" + strings.Repeat("x", valueBytes)})
			r = resource("NotificationEndpoint", refs[i], api.DriverConfig{Type: "log", Config: config})
		case i == endpoints:
			r = resource("NotificationGroup", "large-group", api.NotificationGroupSpec{EndpointRefs: refs})
		default:
			r = collectionMonitor("large-consumer", "https://example.test/health")
			var spec api.MonitorSpec
			_ = json.Unmarshal(r.Spec, &spec)
			spec.Notifications = &map[string]api.AlertRule{"red": {GroupRef: api.Pointer("large-group"), NotifyType: api.Pointer("log")}}
			r.Spec, _ = json.Marshal(spec)
		}
		raw, _ := json.Marshal(r)
		return persistence.CatalogKey{Kind: r.Kind, ID: r.Metadata.ID}, raw
	}, nil, nil)
	index := f.store.Status().CommittedIndex
	result, err, source := stagedValidationRun(t, &f, collectionReadAll)
	if err != nil || !result.Valid || len(result.Order) != endpoints+2 || source.peakBytes > collectionSourcePlaintextBudget || f.store.Status().CommittedIndex != index {
		t.Fatal("large notification closure retained aggregate resources or wrote state", result.Valid, source.peakBytes, err)
	}
	t.Logf("validated notification closure exceeding %d plaintext bytes; peak accounted borrow/scratch %d bytes (not RSS)", endpoints*valueBytes, source.peakBytes)
}
