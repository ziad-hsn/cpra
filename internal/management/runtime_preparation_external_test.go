//go:build externaljobs

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
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/controller/entities"
	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// Public external configuration remains closed until dispatch is connected.
// Install an encrypted, reference-checked fixture through the real commit path
// so runtime preparation can be verified without weakening that admission gate.
func commitRuntimeReference(t *testing.T, c *Catalog, input api.Resource) api.Resource {
	t.Helper()
	view, err := c.store.CatalogSnapshotContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	prepared := prepareReferenceResource(t, c, input)
	m := &prepared.mutation
	if old, ok := view.Get(m.Record.Key); ok {
		m.Create = false
		m.ExpectedUID, m.ExpectedRevision, m.ExpectedDependentsVersion = old.UID, old.Revision, old.DependentsVersion
		m.Record.UID, m.Record.Generation, m.Record.CreatedAt = old.UID, old.Generation+1, old.CreatedAt
		prepared.resource.Metadata.UID, prepared.resource.Metadata.Generation = old.UID, int64(m.Record.Generation)
	}
	m.Record.References, err = directReferences(prepared.resource)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range m.Record.References {
		dependency, ok := view.Get(key)
		if !ok {
			t.Fatal("missing fixture dependency", key)
		}
		m.Conditions = append(m.Conditions, persistence.CatalogCondition{Key: key, UID: dependency.UID, Revision: dependency.Revision})
	}
	if err := api.ValidateResource(prepared.resource); err != nil {
		t.Fatal("invalid runtime fixture", err)
	}
	raw, err := json.Marshal(prepared.resource)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(raw)
	m.Record.Payload, err = c.sealer.Seal(t.Context(), m.Record.Binding(c.storeID), raw)
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.CommitAs(t.Context(), prepared, "team/operator")
	if err != nil {
		t.Fatal(err)
	}
	return result.Resource
}

func prepareRuntimeReference(t *testing.T, c *Catalog, id string) RuntimePreparation {
	t.Helper()
	view, err := c.SnapshotContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.PrepareRuntime(t.Context(), view, id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRuntimeExternalPreparationPreservesMixedSlots(t *testing.T) {
	c, _ := jobTypeCatalog(t)
	for _, category := range []string{"check", "recovery", "notification"} {
		createReferenceJobType(t, c, category)
	}
	var calls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer target.Close()
	config, _ := json.Marshal(map[string]string{"url": target.URL})
	local := commitRuntimeReference(t, c, resource("NotificationEndpoint", "local", api.DriverConfig{Type: "webhook", Config: config}))
	remote := commitRuntimeReference(t, c, resource("NotificationEndpoint", "remote", runtimeExternalDriver("notification")))
	driver := runtimeExternalDriver("notification")
	rules := map[string]api.AlertRule{
		"red":    {EndpointRefs: api.Pointer([]string{"remote", "local"})},
		"yellow": {EndpointRefs: api.Pointer([]string{"remote", "local"})},
		"green":  {Driver: &driver},
	}
	input := resource("Monitor", "monitor", api.MonitorSpec{
		Check:    api.CheckSpec{Driver: runtimeExternalDriver("check"), Interval: "60s", Timeout: "5s", Retries: api.Pointer(int64(2))},
		Recovery: &api.RecoverySpec{Driver: runtimeExternalDriver("recovery")}, Notifications: &rules,
	})
	committed := commitRuntimeReference(t, c, input)
	p := prepareRuntimeReference(t, c, "monitor")
	if len(p.ExternalBindings) != 4 || p.Monitor.Pulse.Retries != 2 || len(p.Conditions) != 3 {
		t.Fatal("bindings duplicated, retry setting lost or dependency guard missing")
	}
	if len(p.NotificationTargets["red"]) != 2 || p.NotificationTargets["red"][0].EndpointUID != remote.Metadata.UID || p.NotificationTargets["red"][1].EndpointUID != local.Metadata.UID {
		t.Fatal("notification ordering or destination incarnation changed")
	}
	for _, binding := range p.ExternalBindings {
		if binding.Key.JobType != binding.Descriptor.Identity() || !bytes.Contains(binding.Descriptor.Parameters(), []byte("private-parameter-canary")) {
			t.Fatal("authenticated parameters or contract lost")
		}
		if binding.Key.SourceKind == "Monitor" && (binding.Key.SourceUID != committed.Metadata.UID || binding.Key.SourceRevision != committed.Metadata.ResourceVersion) {
			t.Fatal("monitor binding lost its committed source")
		}
	}
	if _, err := json.Marshal(p); err == nil {
		t.Fatal("private runtime configuration became serializable")
	}
	for _, text := range []string{fmt.Sprintf("%v", p), fmt.Sprintf("%+v", p), fmt.Sprintf("%#v", p)} {
		if strings.Contains(text, "private-") {
			t.Fatal("runtime formatting leaked private input")
		}
	}
	prepared, err := entities.PrepareMonitorExternal(p.Monitor, p.Endpoints, p.NotificationGroups, p.ExternalBindings)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	wantCheck := p.Monitor.Pulse.Config.(*manifest.ExternalPulseConfig).Key
	// Preparation transfers detached input, not pointers into the catalog result.
	p.Monitor.Pulse.Config.(*manifest.ExternalPulseConfig).Key.SourceUID = "changed"
	for i := range p.ExternalBindings {
		p.ExternalBindings[i].Key.SourceUID = "changed"
		clear(p.ExternalBindings[i].Descriptor.Parameters())
	}
	world := ecs.NewWorld()
	manager := entities.NewEntityManager(&world)
	ent, err := manager.Install(prepared, &world)
	if err != nil {
		t.Fatal(err)
	}
	storage := manager.JobStorage.Get(ent)
	if storage.PulseJob != nil || storage.InterventionJob != nil || storage.ExternalJobs == nil || storage.ExternalJobs.Check.Key != wantCheck {
		t.Fatal("external preparation became local execution or borrowed mutable input")
	}
	for _, color := range []string{"red", "yellow"} {
		if len(storage.CodeJobs[color]) != 2 || storage.CodeJobs[color][0] != nil || storage.CodeJobs[color][1] == nil || storage.ExternalJobs.Notifications[color][0].Key.SourceUID != remote.Metadata.UID || storage.ExternalJobs.Notifications[color][1] != nil {
			t.Fatal("mixed notification positions changed", color)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("runtime preparation or ECS installation called a provider")
	}
	input.Metadata.ID = "not-yet-configured"
	if _, err := c.Prepare(t.Context(), input, "", true); !errors.Is(err, ErrValidation) {
		t.Fatal("runtime preparation opened public external admission", err)
	}
}

func TestRuntimeExternalPreparationSelectsOnlyRoutedEndpoints(t *testing.T) {
	c, _ := jobTypeCatalog(t)
	createReferenceJobType(t, c, "check")
	createReferenceJobType(t, c, "notification")
	commitRuntimeReference(t, c, resource("NotificationEndpoint", "remote", runtimeExternalDriver("notification")))
	logPath := filepath.Join(t.TempDir(), "must-not-exist.log")
	logConfig, _ := json.Marshal(map[string]string{"file": logPath})
	commitRuntimeReference(t, c, resource("NotificationEndpoint", "local", api.DriverConfig{Type: "log", Config: logConfig}))
	commitRuntimeReference(t, c, resource("NotificationGroup", "destinations", api.NotificationGroupSpec{EndpointRefs: []string{"remote", "local"}}))
	rules := map[string]api.AlertRule{"red": {NotifyType: api.Pointer("log"), GroupRef: api.Pointer("destinations")}}
	commitRuntimeReference(t, c, resource("Monitor", "monitor", api.MonitorSpec{Check: api.CheckSpec{Driver: runtimeExternalDriver("check"), Interval: "60s", Timeout: "5s"}, Notifications: &rules}))
	p := prepareRuntimeReference(t, c, "monitor")
	if len(p.ExternalBindings) != 1 || p.ExternalBindings[0].Key.Slot != "check" || len(p.Endpoints) != 1 || p.Endpoints["local"].Type != "log" {
		t.Fatal("an unrouted external endpoint became executable input")
	}
	prepared, err := entities.PrepareMonitorExternal(p.Monitor, p.Endpoints, p.NotificationGroups, p.ExternalBindings)
	if err != nil {
		t.Fatal(err)
	}
	_ = prepared.Close()
	if _, err := os.Stat(logPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("preparation created a notification log", err)
	}
}

func TestRuntimeExternalContentIdentityAndSourceGuards(t *testing.T) {
	c, _ := jobTypeCatalog(t)
	jobType := createReferenceJobType(t, c, "check")
	input := externalReferenceResource("check", false)
	commitRuntimeReference(t, c, input)
	first := prepareRuntimeReference(t, c, "monitor")
	input.Metadata.Name = api.Pointer("Renamed display label")
	commitRuntimeReference(t, c, input)
	renamed := prepareRuntimeReference(t, c, "monitor")
	if first.ExecutionRevision != renamed.ExecutionRevision || first.ProjectionVersion == renamed.ProjectionVersion || first.ResourceVersion == renamed.ResourceVersion || renamed.Monitor.Name != *input.Metadata.Name {
		t.Fatal("display change changed content identity or lost exact revision guards")
	}
	if first.ExternalBindings[0].Key.SourceRevision == renamed.ExternalBindings[0].Key.SourceRevision {
		t.Fatal("old source binding was reused after a conditional change")
	}
	jobType.Metadata.Name = api.Pointer("Renamed job type")
	commitTestJobType(t, c, jobType, jobType.Metadata.ResourceVersion, false)
	if got := prepareRuntimeReference(t, c, "monitor"); got.ExecutionRevision != renamed.ExecutionRevision || got.ExternalBindings[0].Key.JobType != renamed.ExternalBindings[0].Key.JobType {
		t.Fatal("mutable type metadata replaced the original contract")
	}
	input.Spec = bytes.ReplaceAll(input.Spec, []byte("private-parameter-canary"), []byte("changed-parameter"))
	commitRuntimeReference(t, c, input)
	changed := prepareRuntimeReference(t, c, "monitor")
	if changed.ExecutionRevision == renamed.ExecutionRevision || !bytes.Contains(changed.ExternalBindings[0].Descriptor.Parameters(), []byte("changed-parameter")) || !bytes.Contains(first.ExternalBindings[0].Descriptor.Parameters(), []byte("private-parameter-canary")) {
		t.Fatal("parameter edit reused an execution identity or changed earlier owned input")
	}
	view, err := c.SnapshotContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.PrepareRuntime(ctx, view, "monitor"); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled runtime preparation continued", err)
	}
}

func TestRuntimeExternalPreparationByteLimit(t *testing.T) {
	c, _ := jobTypeCatalog(t)
	createReferenceJobType(t, c, "check")
	prepared := prepareReferenceResource(t, c, externalReferenceResource("check", false))
	resource, err := c.open(t.Context(), prepared.mutation.Record)
	if err != nil {
		t.Fatal(err)
	}
	p := RuntimePreparation{PreparationBytes: 32 << 20}
	value, handled, err := p.resolveRuntimeExtension(t.Context(), resource, prepared.mutation.Record, "check", "check", "external")
	if !handled || value != nil || !errors.Is(err, ErrGraphLimit) || len(p.ExternalBindings) != 0 || p.PreparationBytes != 32<<20 {
		t.Fatal("full preparation budget retained more private input", err)
	}
}

func TestRuntimeExternalContentIdentityPreservesJSONIntegers(t *testing.T) {
	key := persistence.CatalogKey{Kind: "Monitor", ID: "monitor"}
	p := RuntimePreparation{Conditions: []persistence.CatalogCondition{{Key: key}}}
	p.ExternalBindings = []manifest.ExternalRuntimeBinding{{}}
	resources := map[persistence.CatalogKey]api.Resource{key: {Spec: json.RawMessage(`{"counter":9007199254740992}`)}}
	records := map[persistence.CatalogKey]persistence.CatalogRecord{key: {UID: "source-incarnation"}}
	first, err := p.configurationRevision(t.Context(), resources, records)
	if err != nil {
		t.Fatal(err)
	}
	resources[key] = api.Resource{Spec: json.RawMessage(`{"counter":9007199254740993}`)}
	second, err := p.configurationRevision(t.Context(), resources, records)
	if err != nil || first == second {
		t.Fatal("different integer parameters shared an execution identity", err)
	}
}
