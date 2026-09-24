package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// Verify the checked-in tutorial resources against the actual encrypted catalog
// and its dependency/driver validation. No controller or provider job is started.
func TestManagementDocumentedResourcesPassCatalogValidation(t *testing.T) {
	ctx := context.Background()
	config := runtimeconfig.Default()
	config.Storage.Mode = "memory"
	store, err := persistence.Open(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	wrapper, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{37}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secureconfig.NewSealer(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := management.NewCatalog(store, sealer)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	create := func(resource api.Resource) {
		t.Helper()
		prepared, err := catalog.Prepare(ctx, resource, "", true)
		if err != nil {
			t.Fatalf("prepare example %s: %v", resource.Kind, err)
		}
		if _, err := catalog.CommitAs(ctx, prepared, "example-operator"); err != nil {
			t.Fatalf("commit example %s: %v", resource.Kind, err)
		}
	}
	create(api.Resource{APIVersion: api.APIVersion, Kind: "Credential", Metadata: api.Metadata{ID: "oncall-webhook-url"}, Spec: json.RawMessage(`{"value":"https://example.invalid/hooks"}`)})
	for _, name := range []string{"endpoint.yaml", "recipient.yaml", "group.yaml", "monitor.yaml"} {
		command := NewRootCommand()
		command.SetContext(ctx)
		resource, err := readManagementResource(command, filepath.Join("..", "..", "..", "examples", "cpractl", name))
		if err != nil {
			t.Fatalf("decode example %s: %v", name, err)
		}
		create(resource)
	}
	if err := catalog.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	monitor, err := catalog.Get(ctx, "Monitor", "service-api")
	if err != nil {
		t.Fatal(err)
	}
	var spec api.MonitorSpec
	if err := json.Unmarshal(monitor.Spec, &spec); err != nil || spec.Enabled == nil || *spec.Enabled {
		t.Fatal("tutorial monitor must remain explicitly disabled")
	}
}
