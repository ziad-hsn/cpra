package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type routingLookupFunc func(context.Context, persistence.CatalogKey, func(*api.Resource) error) (bool, error)

func (f routingLookupFunc) withResource(ctx context.Context, key persistence.CatalogKey, fn func(*api.Resource) error) (bool, error) {
	return f(ctx, key, fn)
}

func routingResourceMap(t *testing.T, catalog NotificationCatalog) mapResourceLookup {
	t.Helper()
	resources := mapResourceLookup{}
	add := func(value any) {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var resource api.Resource
		if err := json.Unmarshal(raw, &resource); err != nil {
			t.Fatal(err)
		}
		resources[persistence.CatalogKey{Kind: resource.Kind, ID: resource.Metadata.ID}] = resource
	}
	for _, value := range catalog.Endpoints {
		add(value)
	}
	for _, value := range catalog.Recipients {
		add(value)
	}
	for _, value := range catalog.Groups {
		add(value)
	}
	return resources
}

func TestRoutingLookupParityAndScopedBorrow(t *testing.T) {
	catalog := routingCatalog()
	resources := routingResourceMap(t, catalog)
	for _, rule := range []api.AlertRule{
		{NotifyType: api.Pointer("email"), RecipientRefs: api.Pointer([]string{"alice"}), GroupRef: api.Pointer("ops")},
		{NotifyType: api.Pointer("email"), GroupRef: api.Pointer("ops")},
		{NotifyType: api.Pointer("twilio"), GroupRef: api.Pointer("ops")},
		{NotifyType: api.Pointer("email"), RecipientRefs: api.Pointer([]string{"alice", "alice"})},
		{NotifyType: api.Pointer("email"), RecipientRefs: api.Pointer([]string{})},
		{NotifyType: api.Pointer("slack"), GroupRef: api.Pointer("legacy")},
		{GroupRef: api.Pointer("legacy")},
		{GroupRef: api.Pointer("ops")},
		{GroupRef: api.Pointer("missing")},
		{EndpointRefs: api.Pointer([]string{"mail-b", "mail-a", "mail-b"})},
		{Driver: &api.DriverConfig{Type: "log", Config: json.RawMessage(`{"file":"not-opened.log"}`)}},
		{Dispatch: api.Pointer(true)},
	} {
		active := 0
		lookup := routingLookupFunc(func(ctx context.Context, key persistence.CatalogKey, fn func(*api.Resource) error) (bool, error) {
			return resources.withResource(ctx, key, func(source *api.Resource) error {
				borrowed := *source
				borrowed.Spec = append(json.RawMessage(nil), source.Spec...)
				active++
				defer func() { clear(borrowed.Spec); active-- }()
				return fn(&borrowed)
			})
		})
		want, wantErr := ResolveNotificationRule(rule, catalog)
		got, gotErr := resolveNotificationRuleLookup(context.Background(), rule, lookup, nil)
		if fmt.Sprint(wantErr) != fmt.Sprint(gotErr) || !reflect.DeepEqual(got, want) {
			t.Fatalf("map/lookup resolution differ: want=%+v %v got=%+v %v", want, wantErr, got, gotErr)
		}
		validationErr := validateNotificationRuleLookup(context.Background(), rule, lookup, nil, nil)
		if fmt.Sprint(validationErr) != fmt.Sprint(wantErr) {
			t.Fatalf("validation and resolution differ: %v versus %v", validationErr, wantErr)
		}
		if active != 0 {
			t.Fatal("borrowed resources remained active")
		}
		// The collected result survives destruction of every scoped raw spec.
		if wantErr == nil && rule.Driver != nil && string(got.InlineDriver.Config) != string(rule.Driver.Config) {
			t.Fatal("owned inline result did not survive scope exit")
		}
	}
}

func TestRoutingLookupUnreferencedDefinitionsAndMonitorReferences(t *testing.T) {
	for _, tc := range []struct {
		name string
		kind string
		spec any
	}{
		{"empty group", "NotificationGroup", api.NotificationGroupSpec{}},
		{"duplicate group member", "NotificationGroup", api.NotificationGroupSpec{EndpointRefs: []string{"mail-a", "mail-a"}}},
		{"missing group member", "NotificationGroup", api.NotificationGroupSpec{RecipientRefs: []string{"absent"}}},
		{"empty recipient", "Recipient", api.RecipientSpec{}},
		{"duplicate recipient endpoint", "Recipient", api.RecipientSpec{EndpointRefs: []string{"mail-a", "mail-a"}}},
		{"missing recipient endpoint", "Recipient", api.RecipientSpec{EndpointRefs: []string{"absent"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resources := routingResourceMap(t, routingCatalog())
			raw, err := json.Marshal(tc.spec)
			if err != nil {
				t.Fatal(err)
			}
			key := persistence.CatalogKey{Kind: tc.kind, ID: "unused"}
			resources[key] = api.Resource{APIVersion: api.APIVersion, Kind: tc.kind, Metadata: api.Metadata{ID: key.ID}, Spec: raw}
			if err := validateNotificationDefinitionLookup(context.Background(), key, resources, nil, nil); err == nil {
				t.Fatal("invalid unreferenced definition accepted")
			}
		})
	}
	monitor := api.Monitor{Metadata: api.Metadata{ID: "monitor"}, Spec: api.MonitorSpec{NotificationGroupRefs: api.Pointer([]string{"absent"})}}
	if err := validateNotificationMonitorLookup(context.Background(), monitor, routingResourceMap(t, routingCatalog()), nil, nil); err == nil || !strings.Contains(err.Error(), "missing notification group") {
		t.Fatalf("standalone monitor reference not validated: %v", err)
	}
}

// This fixture generates each endpoint only when borrowed. It does not keep a
// resource map, decoded catalog, or provider configuration slice across lookups.
func lazyRoutingLookup(t *testing.T, count, valueBytes int, sourceReserve ...reserveResourceScratch) (resourceLookup, func() (total, peak, active int)) {
	t.Helper()
	refs := make([]string, count)
	for i := range refs {
		refs[i] = fmt.Sprintf("endpoint-%03d", i)
	}
	total, peak, active := 0, 0, 0
	lookup := routingLookupFunc(func(ctx context.Context, key persistence.CatalogKey, fn func(*api.Resource) error) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		var spec any
		switch key.Kind {
		case "NotificationGroup":
			spec = api.NotificationGroupSpec{EndpointRefs: refs}
		case "NotificationEndpoint":
			config, err := json.Marshal(map[string]string{"file": strings.Repeat("x", valueBytes)})
			if err != nil {
				t.Fatal(err)
			}
			defer clear(config)
			spec = api.DriverConfig{Type: "log", Config: config}
		default:
			return false, nil
		}
		raw, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		if len(sourceReserve) != 0 {
			release, err := sourceReserve[0](3*len(raw) + 1024)
			if err != nil {
				clear(raw)
				return true, err
			}
			defer release()
		}
		total += len(raw)
		active += len(raw)
		peak = max(peak, active)
		defer func() { active -= len(raw); clear(raw) }()
		resource := api.Resource{APIVersion: api.APIVersion, Kind: key.Kind, Metadata: api.Metadata{ID: key.ID}, Spec: raw}
		return true, fn(&resource)
	})
	return lookup, func() (int, int, int) { return total, peak, active }
}

func routingScratchCounter(limit int) (reserveResourceScratch, func() (peak, active int)) {
	peak, active := 0, 0
	reserve := func(n int) (func(), error) {
		if n > limit-active {
			return nil, ErrGraphLimit
		}
		active += n
		peak = max(peak, active)
		return func() { active -= n }, nil
	}
	return reserve, func() (int, int) { return peak, active }
}

func TestRoutingLookupValidatesLargeClosureWithoutRetainingIt(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		valueBytes, minimumTotal int
	}{
		{"512KiB-endpoints", 512 << 10, 32 << 20},
		{"near-1MiB-endpoints", (1 << 20) - 2048, 64 << 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reserve, scratchAccounting := routingScratchCounter(64 << 20)
			lookup, rawAccounting := lazyRoutingLookup(t, 72, tc.valueBytes, reserve)
			visits := 0
			err := validateNotificationRuleLookup(context.Background(), api.AlertRule{GroupRef: api.Pointer("large")}, lookup, func() error { visits++; return nil }, reserve)
			if err != nil {
				t.Fatal(err)
			}
			total, rawPeak, rawActive := rawAccounting()
			scratchPeak, scratchActive := scratchAccounting()
			if total <= tc.minimumTotal || rawPeak >= 1<<20 || rawActive != 0 || scratchActive != 0 || scratchPeak >= 64<<20 || visits != 73 {
				t.Fatalf("unexpected scoped accounting: total=%d rawPeak=%d rawActive=%d scratchPeak=%d scratchActive=%d visits=%d", total, rawPeak, rawActive, scratchPeak, scratchActive, visits)
			}
			t.Logf("validated %d aggregate spec bytes; peak borrowed raw=%d, reserved raw and scratch=%d; logical accounting, not total heap", total, rawPeak, scratchPeak)
		})
	}
}

func TestRoutingLookupWorkCancellationAndScratchFailureRelease(t *testing.T) {
	for _, reason := range []string{"work", "cancel", "scratch"} {
		t.Run(reason, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			lookup, rawAccounting := lazyRoutingLookup(t, 100, 1024)
			reserve, scratchAccounting := routingScratchCounter(1 << 20)
			if reason == "scratch" {
				reserve, scratchAccounting = routingScratchCounter(40 << 10)
			}
			visits := 0
			err := validateNotificationRuleLookup(ctx, api.AlertRule{GroupRef: api.Pointer("large")}, lookup, func() error {
				visits++
				if visits == 10 {
					if reason == "cancel" {
						cancel()
					} else if reason == "work" {
						return ErrGraphLimit
					}
				}
				return nil
			}, reserve)
			want := ErrGraphLimit
			if reason == "cancel" {
				want = context.Canceled
			}
			if !errors.Is(err, want) {
				t.Fatalf("wanted %v, got %v", want, err)
			}
			if reason != "scratch" && visits != 10 {
				t.Fatalf("work continued beyond limit: %d", visits)
			}
			_, _, rawActive := rawAccounting()
			_, scratchActive := scratchAccounting()
			if rawActive != 0 || scratchActive != 0 {
				t.Fatal("failed validation retained a resource reservation")
			}
		})
	}
}

func TestRoutingLookupRepeatedTargetsStillConsumeWork(t *testing.T) {
	catalog := routingCatalog()
	rule := api.AlertRule{NotifyType: api.Pointer("email"), RecipientRefs: api.Pointer([]string{"alice"}), GroupRef: api.Pointer("ops")}
	resolveVisits, validateVisits := 0, 0
	result, err := resolveNotificationRuleLookup(context.Background(), rule, notificationCatalogLookup{catalog}, func() error { resolveVisits++; return nil })
	if err != nil || len(result.Targets) != 2 {
		t.Fatalf("incarnation dedup changed: %+v %v", result, err)
	}
	if err := validateNotificationRuleLookup(context.Background(), rule, notificationCatalogLookup{catalog}, func() error { validateVisits++; return nil }, nil); err != nil {
		t.Fatal(err)
	}
	if resolveVisits != validateVisits || validateVisits != 13 {
		t.Fatalf("dedup skipped repeated expansion work: resolve=%d validate=%d", resolveVisits, validateVisits)
	}
}

func TestRoutingScratchBoundsBeforeReservation(t *testing.T) {
	for _, input := range [][2]int{{-1, 0}, {api.MaxResourceBytes + 1, 0}, {0, -1}, {0, api.MaxResourceBytes + 1}, {int(^uint(0) >> 1), 1}} {
		called := false
		_, err := reserveRoutingScratch(func(int) (func(), error) { called = true; return func() {}, nil }, input[0], input[1])
		if !errors.Is(err, ErrGraphLimit) || called {
			t.Fatalf("unsafe size reached reservation: %v, %v", input, err)
		}
	}
}

func TestRoutingCatalogAdapterPreservesStatusValidation(t *testing.T) {
	largeStatus, err := json.Marshal(strings.Repeat("s", api.MaxResourceBytes))
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"NotificationEndpoint", "Recipient", "NotificationGroup"} {
		for _, status := range []json.RawMessage{json.RawMessage(`{"broken":`), largeStatus} {
			catalog := routingCatalog()
			before := string(status)
			switch kind {
			case "NotificationEndpoint":
				value := catalog.Endpoints["mail-a"]
				value.Status = status
				catalog.Endpoints["mail-a"] = value
			case "Recipient":
				value := catalog.Recipients["alice"]
				value.Status = status
				catalog.Recipients["alice"] = value
			case "NotificationGroup":
				value := catalog.Groups["ops"]
				value.Status = status
				catalog.Groups["ops"] = value
			}
			if err := ValidateNotificationGraph(catalog, nil); err == nil {
				t.Fatalf("map adapter discarded invalid/oversized status for %s", kind)
			}
			if string(status) != before {
				t.Fatal("map adapter cleared caller-owned status")
			}
		}
	}
}
