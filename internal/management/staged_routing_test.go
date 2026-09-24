package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func stagedRoutingValidator(t *testing.T, f *collectionSourceFixture) *stagedValidator {
	t.Helper()
	source := f.open(t, nil)
	captured, err := f.catalog.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	v := &stagedValidator{catalog: f.catalog, captured: captured, source: source, options: collectionReadAll,
		desired: map[persistence.CatalogKey]stagedValidationVersion{}, live: map[persistence.CatalogKey]stagedValidationVersion{},
		item: map[persistence.CatalogKey]int{}, cause: map[persistence.CatalogKey]int{}, reverseGuards: map[persistence.CatalogKey]uint64{}}
	if err := source.walk(context.Background(), func(ref stagedItemRef, r *api.Resource) error { return v.ingest(context.Background(), ref, r) }); err != nil {
		t.Fatal(err)
	}
	if err := v.expand(context.Background()); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestStagedRoutingMetadataSelectsAuthenticatedVersionWithoutReopening(t *testing.T) {
	const secret = "https://example.test/private-staged-hook"
	input := resource("NotificationEndpoint", "chat", api.DriverConfig{Type: "slack", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"hook": "hook"})})
	f := stagedValidationFixture(t, input, resource("Credential", "hook", api.CredentialSpec{Value: api.Pointer(secret)}))
	old := createResource(t, f.catalog, resource("NotificationEndpoint", "chat", api.DriverConfig{Type: "log", Config: json.RawMessage(`{"file":"never-opened.log"}`)}))
	v := stagedRoutingValidator(t, &f)
	key := persistence.CatalogKey{Kind: input.Kind, ID: input.Metadata.ID}
	wrapper := collectionBlockSealing(t, f.catalog)
	for _, desired := range []bool{false, true} {
		lookup := stagedSelectedLookup{validator: v, selected: map[persistence.CatalogKey]bool{key: desired}}
		want := "log"
		if desired {
			want = "slack"
		}
		called := false
		if err := withRoutingEndpoint(context.Background(), lookup, key, nil, v.source.reserve, func(meta api.Metadata, driver string) error {
			called = true
			if driver != want || meta.ID != key.ID || meta.UID != old.Metadata.UID || meta.ResourceVersion != old.Metadata.ResourceVersion {
				t.Fatal("selected endpoint identity/type changed", meta, driver)
			}
			return nil
		}); err != nil || !called {
			t.Fatal("authenticated endpoint unavailable", called, err)
		}
		if exists, err := routingExists(context.Background(), lookup, key, nil); !exists || err != nil {
			t.Fatal("selected endpoint unavailable", exists, err)
		}
	}
	if wrapper.opens.Load() != 0 || v.source.liveBytes != 0 || strings.Contains(fmt.Sprint(v.desired), secret) {
		t.Fatal("routing retained/reopened provider configuration")
	}
	// A live revision change after the snapshot remains a conflict at finish;
	// the metadata cache cannot authorize a plan against the replacement.
	base, _ := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
	f.catalog.sealer, _ = secureconfig.NewSealer(base)
	replacement := resource("NotificationEndpoint", "chat", api.DriverConfig{Type: "log", Config: json.RawMessage(`{"file":"other.log"}`)})
	replacement.Metadata = old.Metadata
	prepared, err := f.catalog.Prepare(context.Background(), replacement, old.Metadata.ResourceVersion, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.catalog.Commit(context.Background(), prepared); err != nil {
		t.Fatal(err)
	}
	if err := v.finish(context.Background(), []persistence.CatalogKey{key}); !errors.Is(err, persistence.ErrCatalogConflict) {
		t.Fatal("routing metadata bypassed captured revision fence", err)
	}
}

func TestStagedRoutingMetadataHonorsAuthorityLifetimeAndWorkBounds(t *testing.T) {
	for _, mode := range []string{"denied", "source-denied", "canceled", "cancel-in-authorizer", "expired", "closed", "work-limit", "missing-version"} {
		t.Run(mode, func(t *testing.T) {
			f := stagedValidationFixture(t, resource("NotificationEndpoint", "chat", api.DriverConfig{Type: "log", Config: json.RawMessage(`{}`)}))
			v := stagedRoutingValidator(t, &f)
			key := f.items[0].Key
			lookup := stagedSelectedLookup{validator: v, selected: map[persistence.CatalogKey]bool{key: true}}
			wrapper := collectionBlockSealing(t, f.catalog)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := error(errCollectionReadDenied)
			switch mode {
			case "denied":
				v.options.CanRead = func(persistence.CatalogKey) bool { return false }
			case "source-denied":
				v.source.canRead = func(persistence.CatalogKey) bool { return false }
			case "canceled":
				cancel()
				want = context.Canceled
			case "cancel-in-authorizer":
				v.options.CanRead = func(persistence.CatalogKey) bool { cancel(); return true }
				want = context.Canceled
			case "expired":
				f.at = f.head.ExpiresAt
				want = persistence.ErrOperationExpired
			case "closed":
				v.source.close()
				want = ErrUnavailable
			case "work-limit":
				v.visits = collectionValidationVisits
				want = ErrGraphLimit
			case "missing-version":
				delete(v.desired, key)
				want = ErrValidation
			}
			called := false
			if _, err := lookup.withRoutingEndpoint(ctx, key, func(api.Metadata, string) error { called = true; return nil }); !errors.Is(err, want) || called {
				t.Fatal("invalid metadata borrow", called, err)
			}
			if exists, err := lookup.routingExists(ctx, key); !errors.Is(err, want) || exists {
				t.Fatal("invalid existence lookup", exists, err)
			}
			if mode == "denied" || mode == "source-denied" {
				absent := persistence.CatalogKey{Kind: "NotificationEndpoint", ID: "absent"}
				if exists, err := lookup.routingExists(ctx, absent); !errors.Is(err, want) || exists {
					t.Fatal("authorization revealed absence", exists, err)
				}
			}
			if wrapper.opens.Load() != 0 || v.source.liveBytes != 0 {
				t.Fatal("denied metadata read acquired provider data")
			}
		})
	}
}

func TestStagedRoutingMetadataChecksAfterBorrow(t *testing.T) {
	for _, mode := range []string{"cancel", "expire"} {
		t.Run(mode, func(t *testing.T) {
			f := stagedValidationFixture(t, resource("NotificationEndpoint", "chat", api.DriverConfig{Type: "log", Config: json.RawMessage(`{}`)}))
			v := stagedRoutingValidator(t, &f)
			key := f.items[0].Key
			lookup := stagedSelectedLookup{validator: v, selected: map[persistence.CatalogKey]bool{key: true}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := error(context.Canceled)
			if mode == "expire" {
				want = persistence.ErrOperationExpired
			}
			_, err := lookup.withRoutingEndpoint(ctx, key, func(api.Metadata, string) error {
				if mode == "expire" {
					v.source.now = func() time.Time { return f.head.ExpiresAt }
				} else {
					cancel()
				}
				return nil
			})
			if !errors.Is(err, want) || v.source.liveBytes != 0 {
				t.Fatal("borrow escaped cancellation/expiry", err)
			}
		})
	}
}

func TestStagedRoutingMetadataDoesNotReplaceDriverValidation(t *testing.T) {
	input := resource("NotificationEndpoint", "chat", api.DriverConfig{Type: "slack", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"hook": "hook"})})
	f := stagedValidationFixture(t, input, resource("Credential", "hook", api.CredentialSpec{Value: api.Pointer("ftp://invalid.example/hook")}))
	v := stagedRoutingValidator(t, &f)
	key := f.items[0].Key
	lookup := stagedSelectedLookup{validator: v, selected: map[persistence.CatalogKey]bool{key: true}}
	if _, err := lookup.withRoutingEndpoint(context.Background(), key, func(_ api.Metadata, driver string) error {
		if driver != "slack" {
			t.Fatal("schema-valid driver type missing")
		}
		return nil
	}); err != nil {
		t.Fatal("fixture did not reach routing metadata", err)
	}
	result, err, source := stagedValidationRun(t, &f, collectionReadAll)
	if !errors.Is(err, ErrValidation) || result.Valid || source.liveBytes != 0 {
		t.Fatal("routing metadata accepted invalid resolved driver", result.Valid, err)
	}
}

func TestStagedRoutingSharedEndpointsBoundConfigurationReads(t *testing.T) {
	const endpoints, monitors = 4, 2
	var inputs []api.Resource
	var refs []string
	for i := range endpoints {
		id := fmt.Sprintf("endpoint-%03d", i)
		refs = append(refs, id)
		inputs = append(inputs, resource("NotificationEndpoint", id, api.DriverConfig{Type: "log", Config: json.RawMessage(`{"file":"never-opened.log"}`)}))
	}
	inputs = append(inputs, resource("NotificationGroup", "group", api.NotificationGroupSpec{EndpointRefs: refs}))
	for i := range monitors {
		monitor := collectionMonitor(fmt.Sprintf("monitor-%03d", i), "https://example.test/health")
		var spec api.MonitorSpec
		_ = json.Unmarshal(monitor.Spec, &spec)
		spec.Notifications = &map[string]api.AlertRule{"red": {GroupRef: api.Pointer("group"), NotifyType: api.Pointer("log")}}
		monitor.Spec, _ = json.Marshal(spec)
		inputs = append(inputs, monitor)
	}
	f := stagedValidationFixture(t, inputs...)
	base, _ := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{23}, 32))
	opens := make([]int, endpoints)
	// Count exact encrypted endpoint envelopes, not timings or private values.
	f.catalog.sealer, _ = secureconfig.NewSealer(stagedRoutingCountingWrapper{KeyWrapper: base, observe: func(wrapped []byte) {
		for i := range endpoints {
			if bytes.Equal(wrapped, f.items[i].Payload.WrappedKey) {
				opens[i]++
			}
		}
	}})
	index := f.store.Status().CommittedIndex
	result, err, source := stagedValidationRun(t, &f, collectionReadAll)
	if err != nil || !result.Valid || source.liveBytes != 0 || f.store.Status().CommittedIndex != index {
		t.Fatal("shared graph validation failed or wrote state", result.Valid, err)
	}
	// Initial ingestion, complete graph, endpoint's own prefix, group prefix,
	// and one dependency closure for each monitor. Routing reads add no decrypts.
	for i, got := range opens {
		if got != 4+monitors {
			t.Fatalf("endpoint %d opened %d times; want %d bounded validation reads", i, got, 4+monitors)
		}
	}
}

type stagedRoutingCountingWrapper struct {
	secureconfig.KeyWrapper
	observe func([]byte)
}

func (w stagedRoutingCountingWrapper) Unwrap(ctx context.Context, wrapped, aad []byte) ([]byte, error) {
	w.observe(wrapped)
	return w.KeyWrapper.Unwrap(ctx, wrapped, aad)
}
