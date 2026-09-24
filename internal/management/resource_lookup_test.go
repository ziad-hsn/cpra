package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type driverLookupFunc func(context.Context, persistence.CatalogKey, func(*api.Resource) error) (bool, error)

func (f driverLookupFunc) withResource(ctx context.Context, key persistence.CatalogKey, visit func(*api.Resource) error) (bool, error) {
	return f(ctx, key, visit)
}

type driverScratchBudget struct {
	live, peak, calls, releases int
	maximum, rejectCall         int
}

func (b *driverScratchBudget) reserve(size int) (func(), error) {
	b.calls++
	if b.calls == b.rejectCall || size < 0 || b.maximum > 0 && size > b.maximum-b.live {
		return nil, ErrGraphLimit
	}
	b.live += size
	b.peak = max(b.peak, b.live)
	released := false
	return func() {
		if released {
			panic("scratch released twice")
		}
		released = true
		b.live -= size
		b.releases++
	}, nil
}

func TestMapResourceLookupScopeAndCancellation(t *testing.T) {
	key := persistence.CatalogKey{Kind: "Credential", ID: "shared"}
	r := resource(key.Kind, key.ID, api.CredentialSpec{Value: api.Pointer("private")})
	lookup := mapResourceLookup{key: r}
	calls := 0
	visit := func(found *api.Resource) error {
		calls++
		if !bytes.Equal(found.Spec, r.Spec) || &found.Spec[0] != &r.Spec[0] {
			t.Fatal("map lookup must lend, not copy, its immutable spec")
		}
		return nil
	}
	if found, err := lookup.withResource(context.Background(), key, visit); err != nil || !found || calls != 1 {
		t.Fatalf("found=%v calls=%d err=%v", found, calls, err)
	}
	if found, err := lookup.withResource(context.Background(), persistence.CatalogKey{Kind: key.Kind, ID: "absent"}, visit); err != nil || found || calls != 1 {
		t.Fatalf("absent lookup invoked callback: found=%v calls=%d err=%v", found, calls, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if found, err := lookup.withResource(ctx, key, visit); !errors.Is(err, context.Canceled) || found || calls != 1 {
		t.Fatalf("cancelled lookup: found=%v calls=%d err=%v", found, calls, err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	if found, err := lookup.withResource(ctx, key, func(*api.Resource) error { cancel(); return nil }); !found || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation during callback: found=%v err=%v", found, err)
	}
	denied := errors.New("read denied")
	if found, err := lookup.withResource(context.Background(), key, func(*api.Resource) error { return denied }); !found || !errors.Is(err, denied) {
		t.Fatalf("callback error: found=%v err=%v", found, err)
	}
}

func TestResolveDriverScopedParityAndBorrowedLifetime(t *testing.T) {
	for _, test := range []struct {
		name, field, value, expected string
	}{
		{"literal-json-string", "body", `{"password":"<private>"}`, `"{\"password\":\"<private>\"}"`},
		{"literal-null-string", "body", "null", `"null"`},
		{"empty-string", "body", "", `""`},
		{"escaped-string", "body", "<>&\n\u2028\u2029", `"<>&\n\u2028\u2029"`},
		{"structured-map", "headers", `{"Authorization":"Bearer private","X-Order":"<>&"}`, `{"Authorization":"Bearer private","X-Order":"<>&"}`},
		{"structured-array", "expectedStatus", "[200,204]", "[200,204]"},
		{"integer", "retries", "3", "3"},
		{"boolean", "insecureSkipVerify", "false", "false"},
	} {
		t.Run(test.name, func(t *testing.T) {
			key := persistence.CatalogKey{Kind: "Credential", ID: "shared"}
			credential := resource(key.Kind, key.ID, api.CredentialSpec{Value: &test.value})
			original := slices.Clone(credential.Spec)
			newDriver := func() api.DriverConfig {
				return api.DriverConfig{Type: "http", Config: json.RawMessage(`{"url":"https://example.test","method":"GET"}`), CredentialRefs: api.Pointer(map[string]string{test.field: key.ID})}
			}
			mapped := newDriver()
			if err := resolveDriver(&mapped, "check", map[persistence.CatalogKey]api.Resource{key: credential}); err != nil {
				t.Fatal(err)
			}
			borrowed := driverLookupFunc(func(ctx context.Context, requested persistence.CatalogKey, visit func(*api.Resource) error) (bool, error) {
				if requested != key {
					t.Fatalf("unexpected lookup: %v", requested)
				}
				r := credential
				r.Spec = slices.Clone(r.Spec)
				defer clear(r.Spec) // Invalidates all borrowed bytes immediately.
				return true, visit(&r)
			})
			budget := &driverScratchBudget{maximum: 1 << 20}
			scoped := newDriver()
			release, err := resolveDriverWithLookup(context.Background(), &scoped, "check", borrowed, budget.reserve)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(scoped.Config, mapped.Config) || scoped.CredentialRefs != nil {
				t.Fatal("scoped and existing map resolution differ")
			}
			var fields map[string]json.RawMessage
			if json.Unmarshal(scoped.Config, &fields) != nil || !jsonEqual(fields[test.field], []byte(test.expected)) {
				t.Fatal("literal-versus-structured credential semantics changed")
			}
			if !bytes.Equal(credential.Spec, original) {
				t.Fatal("map credential was modified")
			}
			if budget.live < len(scoped.Config) || budget.releases != budget.calls-1 {
				t.Fatalf("output lost reservation or scratch retained: live=%d calls=%d releases=%d", budget.live, budget.calls, budget.releases)
			}
			clear(scoped.Config)
			release()
			if budget.live != 0 || budget.calls != budget.releases {
				t.Fatalf("output reservation leaked: %+v", budget)
			}
		})
	}
}

func TestResolveDriverScopedSafeFailuresLeaveInputUnchanged(t *testing.T) {
	secret := "never-include-private-provider-value"
	key := persistence.CatalogKey{Kind: "Credential", ID: "shared"}
	for _, test := range []struct {
		name string
		r    api.Resource
		want error
	}{
		{"missing-value", resource(key.Kind, key.ID, api.CredentialSpec{}), ErrUnavailable},
		{"null-value", api.Resource{Kind: key.Kind, Metadata: api.Metadata{ID: key.ID}, Spec: json.RawMessage(`{"value":null}`)}, ErrUnavailable},
		{"wrong-kind", resource("Recipient", key.ID, api.CredentialSpec{Value: &secret}), ErrUnavailable},
		{"wrong-identity", resource(key.Kind, "other", api.CredentialSpec{Value: &secret}), ErrUnavailable},
		{"wrong-value-type", resource(key.Kind, key.ID, map[string]any{"value": map[string]string{"private": secret}}), ErrUnavailable},
		{"unknown-field", resource(key.Kind, key.ID, map[string]string{"value": secret, "extra": secret}), ErrUnavailable},
		{"wrong-driver-type", resource(key.Kind, key.ID, api.CredentialSpec{Value: &secret}), ErrValidation},
	} {
		t.Run(test.name, func(t *testing.T) {
			driver := api.DriverConfig{Type: "http", Config: json.RawMessage(`{"url":"https://example.test"}`), CredentialRefs: api.Pointer(map[string]string{"headers": key.ID})}
			before, _ := json.Marshal(driver)
			budget := &driverScratchBudget{}
			release, err := resolveDriverWithLookup(context.Background(), &driver, "check", mapResourceLookup{key: test.r}, budget.reserve)
			after, _ := json.Marshal(driver)
			if !errors.Is(err, test.want) || release != nil || !bytes.Equal(before, after) || budget.live != 0 {
				t.Fatalf("wrong error or retained state: err=%v release=%v budget=%+v", err, release != nil, budget)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatal("validation error disclosed credential input")
			}
		})
	}
	driver := api.DriverConfig{Type: "http", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"body": key.ID})}
	if _, err := resolveDriverWithLookup(context.Background(), &driver, "check", mapResourceLookup{}, nil); !errors.Is(err, persistence.ErrCatalogDependency) {
		t.Fatalf("missing identity: %v", err)
	}
}

func TestResolveDriverScopedExpansionReservationsAndEveryRejection(t *testing.T) {
	key := persistence.CatalogKey{Kind: "Credential", ID: "shared"}
	value := strings.Repeat("<", 4096)
	r := resource(key.Kind, key.ID, api.CredentialSpec{Value: &value})
	makeDriver := func() api.DriverConfig {
		return api.DriverConfig{Type: "http", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"body": key.ID})}
	}
	baseline := &driverScratchBudget{}
	driver := makeDriver()
	release, err := resolveDriverWithLookup(context.Background(), &driver, "check", mapResourceLookup{key: r}, baseline.reserve)
	if err != nil {
		t.Fatal(err)
	}
	if len(driver.Config) <= len(value)*5 || baseline.live < len(driver.Config) {
		t.Fatal("escaped output was not charged through caller ownership")
	}
	clear(driver.Config)
	release()
	for reject := 1; reject <= baseline.calls; reject++ {
		driver := makeDriver()
		before, _ := json.Marshal(driver)
		budget := &driverScratchBudget{rejectCall: reject}
		release, err := resolveDriverWithLookup(context.Background(), &driver, "check", mapResourceLookup{key: r}, budget.reserve)
		after, _ := json.Marshal(driver)
		if !errors.Is(err, ErrGraphLimit) || release != nil || budget.live != 0 || !bytes.Equal(before, after) {
			t.Fatalf("reservation rejection %d leaked or changed input: err=%v budget=%+v", reject, err, budget)
		}
	}
	// A byte quota, not only the count of credentials, rejects expansion.
	driver = makeDriver()
	bounded := &driverScratchBudget{maximum: len(r.Spec) * 3}
	if _, err := resolveDriverWithLookup(context.Background(), &driver, "check", mapResourceLookup{key: r}, bounded.reserve); !errors.Is(err, ErrGraphLimit) || bounded.live != 0 {
		t.Fatalf("expansion quota not enforced: err=%v budget=%+v", err, bounded)
	}
	// Sequential independent resolutions reuse the capacity after output release.
	bounded = &driverScratchBudget{maximum: baseline.peak}
	for range 20 {
		driver = makeDriver()
		release, err := resolveDriverWithLookup(context.Background(), &driver, "check", mapResourceLookup{key: r}, bounded.reserve)
		if err != nil {
			t.Fatal("sequential resolution accumulated old reservations", err)
		}
		clear(driver.Config)
		release()
	}
	if bounded.live != 0 {
		t.Fatal("sequential reservation leak")
	}
}

func TestResolveDriverScopedCancellationAndLookupError(t *testing.T) {
	key := persistence.CatalogKey{Kind: "Credential", ID: "shared"}
	r := resource(key.Kind, key.ID, api.CredentialSpec{Value: api.Pointer("private")})
	for _, when := range []string{"before", "during-lookup", "after-lookup", "reservation"} {
		t.Run(when, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			lookup := driverLookupFunc(func(_ context.Context, _ persistence.CatalogKey, visit func(*api.Resource) error) (bool, error) {
				calls++
				if when == "during-lookup" {
					cancel()
				}
				err := visit(&r)
				if when == "after-lookup" {
					cancel()
				}
				return true, err
			})
			budget := &driverScratchBudget{}
			reserve := func(n int) (func(), error) {
				release, err := budget.reserve(n)
				if when == "reservation" {
					cancel()
				}
				return release, err
			}
			if when == "before" {
				cancel()
			}
			driver := api.DriverConfig{Type: "http", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"body": key.ID})}
			release, err := resolveDriverWithLookup(ctx, &driver, "check", lookup, reserve)
			if !errors.Is(err, context.Canceled) || release != nil || budget.live != 0 || string(driver.Config) != `{}` || driver.CredentialRefs == nil {
				t.Fatalf("cancellation changed state or leaked: err=%v budget=%+v", err, budget)
			}
			if when == "before" && calls != 0 {
				t.Fatal("lookup started after cancellation")
			}
		})
	}
	denied := errors.New("read denied")
	driver := api.DriverConfig{Type: "http", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"body": key.ID})}
	lookup := driverLookupFunc(func(context.Context, persistence.CatalogKey, func(*api.Resource) error) (bool, error) {
		return false, denied
	})
	if _, err := resolveDriverWithLookup(context.Background(), &driver, "check", lookup, nil); !errors.Is(err, denied) {
		t.Fatalf("lookup error lost identity: %v", err)
	}
}

func TestResolveDriverScopedInlineAndStableLookupOrder(t *testing.T) {
	driver := api.DriverConfig{Type: "http", Config: json.RawMessage(`{"url":"https://example.test"}`)}
	release, err := resolveDriverWithLookup(context.Background(), &driver, "check", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	release()
	var order []string
	lookup := driverLookupFunc(func(_ context.Context, key persistence.CatalogKey, visit func(*api.Resource) error) (bool, error) {
		order = append(order, key.ID)
		r := resource(key.Kind, key.ID, api.CredentialSpec{Value: api.Pointer("literal")})
		return true, visit(&r)
	})
	driver = api.DriverConfig{Type: "http", Config: json.RawMessage(`{}`), CredentialRefs: api.Pointer(map[string]string{"url": "third", "method": "second", "body": "first"})}
	release, err = resolveDriverWithLookup(context.Background(), &driver, "check", lookup, nil)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if !reflect.DeepEqual(order, []string{"first", "second", "third"}) {
		t.Fatalf("nondeterministic credential order: %v", order)
	}
}

func TestDriverScratchArithmeticRejectsOverflowBeforeReservation(t *testing.T) {
	maximum := int(^uint(0) >> 1)
	called := false
	charge := func(int) error { called = true; return nil }
	for _, test := range []struct {
		factor int
		sizes  []int
	}{{6, []int{maximum}}, {1, []int{maximum, 1}}, {1, []int{-1}}, {0, []int{1}}} {
		if err := chargeDriverBytes(context.Background(), charge, test.factor, test.sizes...); !errors.Is(err, ErrGraphLimit) {
			t.Fatalf("unsafe allocation size accepted: %v", err)
		}
	}
	if called {
		t.Fatal("overflow reached reservation callback")
	}
}
