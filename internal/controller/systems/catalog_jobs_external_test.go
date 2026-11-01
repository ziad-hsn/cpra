//go:build externaljobs

package systems

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/controller/entities"
	"github.com/ziad-hsn/cpra/internal/fleetview"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func catalogExternalConfig(t *testing.T) runtimeconfig.Config {
	t.Helper()
	root := t.TempDir()
	cfg := runtimeconfig.Default()
	cfg.Storage.Directory = filepath.Join(root, "state")
	cfg.Management.Enabled = true
	cfg.ExternalJobs.Enabled = true
	cfg.Management.TrustedProxy = &runtimeconfig.ManagementProxy{PublicOrigin: "https://cpra.example.test"}
	cfg.Management.Encryption = &runtimeconfig.ManagementEncryption{Local: &runtimeconfig.ManagementLocalKeys{ActiveKeyFile: filepath.Join(root, "wrapping.key")}}
	return cfg
}

func catalogExternalPreparation(t *testing.T) management.RuntimePreparation {
	t.Helper()
	id := manifest.ExternalJobIdentity{JobTypeID: "custom-check", JobTypeUID: "type-uid", Version: "1", Revision: "type-revision", Category: "check"}
	d, err := manifest.NewExternalJobDescriptor(id, []byte(`{"target":"private-parameter-canary"}`), "local-profile-canary")
	if err != nil {
		t.Fatal(err)
	}
	key := manifest.ExternalRuntimeKey{SourceKind: "Monitor", SourceID: "remote", SourceUID: "monitor-uid", SourceRevision: "source-revision", Slot: "check", JobType: id}
	p := management.RuntimePreparation{Monitor: manifest.Monitor{ID: "remote", Name: "remote", Enabled: true, Pulse: manifest.Pulse{Type: "external", Config: &manifest.ExternalPulseConfig{Key: key}, Interval: time.Minute, Timeout: 5 * time.Second}}, MonitorUID: key.SourceUID, ResourceVersion: key.SourceRevision}
	p.ExternalBindings = []manifest.ExternalRuntimeBinding{{Key: key, Descriptor: d}}
	return p
}

func TestCatalogExternalPreparationRequiresRuntimeOptIn(t *testing.T) {
	p := catalogExternalPreparation(t)
	for _, name := range []string{"disabled", "management-disabled", "memory", "missing-binding", "mismatched-binding"} {
		t.Run(name, func(t *testing.T) {
			cfg := catalogExternalConfig(t)
			input := catalogExternalPreparation(t)
			switch name {
			case "disabled":
				cfg.ExternalJobs.Enabled = false
			case "management-disabled":
				cfg.Management.Enabled = false
			case "memory":
				cfg.Storage.Mode = "memory"
			case "missing-binding":
				input.ExternalBindings = nil
			case "mismatched-binding":
				input.ExternalBindings[0].Key.SourceUID = "different"
			}
			if prepared, err := prepareCatalogMonitor(input, cfg); err == nil || prepared != nil {
				t.Fatal("ineligible external preparation became executable")
			}
		})
	}
	prepared, err := prepareCatalogMonitor(p, catalogExternalConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	world := ecs.NewWorld()
	manager := entities.NewEntityManager(&world)
	ent, err := manager.Install(prepared, &world)
	if err != nil {
		t.Fatal(err)
	}
	storage := manager.JobStorage.Get(ent)
	if storage.PulseJob != nil || storage.ExternalJobs == nil || storage.ExternalJobs.Check.Key != p.ExternalBindings[0].Key {
		t.Fatal("external configuration was lost or became a local executable")
	}
}

func TestCatalogExternalCheckDemandFollowsInstalledJobs(t *testing.T) {
	s, _ := projectionRecoveryOwner(t)
	p := catalogExternalPreparation(t)
	install := func(p management.RuntimePreparation, replace bool) ecs.Entity {
		prepared, err := prepareCatalogMonitor(p, catalogExternalConfig(t))
		if err != nil {
			t.Fatal(err)
		}
		defer prepared.Close()
		ent := s.entities[p.Monitor.ID]
		if replace {
			err = s.catalogRuntime.mapper.Replace(ent, prepared, s.world)
		} else {
			ent, err = s.catalogRuntime.mapper.Install(prepared, s.world)
		}
		if err != nil {
			t.Fatal(err)
		}
		s.entities[p.Monitor.ID] = ent
		for int(ent.ID()) >= len(s.checks) {
			s.checks = append(s.checks, checkSchedule{})
		}
		return ent
	}
	local := p
	local.ExternalBindings = nil
	local.Monitor.Pulse.Type = "http"
	local.Monitor.Pulse.Config = &manifest.PulseHTTPConfig{Url: "http://127.0.0.1:1"}
	ent := install(local, false)
	m := persistence.Monitor{ID: p.Monitor.ID, Policy: persistence.Policy{Enabled: true, Interval: time.Minute}}
	s.projectControls(ent, m)
	if s.PulseArrivalRate() != 1.0/60 || s.activePulseMonitors != 1 {
		t.Fatal("local check was excluded from capacity demand")
	}
	install(p, true)
	for range 2 {
		s.projectControls(ent, m)
		if s.PulseArrivalRate() != 0 || s.activePulseMonitors != 0 {
			t.Fatal("external check inflated local demand or replay drifted the count")
		}
	}
	install(local, true)
	s.projectControls(ent, m)
	if s.PulseArrivalRate() != 1.0/60 || s.activePulseMonitors != 1 {
		t.Fatal("replacing an external check lost local demand")
	}
	m.SnoozedUntil = time.Now().Add(time.Minute)
	s.projectControls(ent, m)
	if s.PulseArrivalRate() != 0 || s.activePulseMonitors != 0 {
		t.Fatal("snoozed check still contributes local demand")
	}
}

// This fixture installs encrypted records through the real state machine while
// public external-resource admission remains closed. No API gate is bypassed in
// production code. References and immutable JobType versions are explicit inputs.
func commitCatalogExternalFixture(t *testing.T, store *persistence.Store, sealer *secureconfig.Sealer, resource api.Resource, refs []persistence.CatalogKey, types []persistence.JobTypeReference) api.Resource {
	t.Helper()
	at := time.Now().UTC()
	r := persistence.CatalogRecord{Key: persistence.CatalogKey{Kind: resource.Kind, ID: resource.Metadata.ID}, UID: uuid.NewString(), Revision: uuid.NewString(), Generation: 1, Purpose: "desired-resource", CreatedAt: at, UpdatedAt: at, References: refs}
	mutation := persistence.CatalogMutation{Record: r, Create: true}
	old, exists, err := store.CatalogGet(r.Key)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		mutation.Create = false
		mutation.ExpectedUID, mutation.ExpectedRevision, mutation.ExpectedDependentsVersion = old.UID, old.Revision, old.DependentsVersion
		r.UID, r.Generation, r.CreatedAt = old.UID, old.Generation+1, old.CreatedAt
	}
	resource.Metadata.UID, resource.Metadata.ResourceVersion, resource.Metadata.Generation = r.UID, r.Revision, int64(r.Generation)
	if err := api.ValidateResource(resource); err != nil {
		t.Fatal(err)
	}
	r.JobTypeReferences, err = persistence.CanonicalJobTypeReferences(types)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range refs {
		dependency, ok, err := store.CatalogGet(ref)
		if err != nil || !ok {
			t.Fatal("missing fixture dependency", ref, err)
		}
		mutation.Conditions = append(mutation.Conditions, persistence.CatalogCondition{Key: ref, UID: dependency.UID, Revision: dependency.Revision})
	}
	raw, err := json.Marshal(resource)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(raw)
	r.Payload, err = sealer.Seal(t.Context(), r.Binding(store.Status().NodeID), raw)
	if err != nil {
		t.Fatal(err)
	}
	mutation.Record = r
	results, err := store.Submit(t.Context(), []persistence.Command{{Kind: "catalog", At: at, Catalog: &mutation}})
	if err != nil || len(results) != 1 || results[0].Err != nil {
		t.Fatalf("commit encrypted fixture: %v %+v", err, results)
	}
	return resource
}

func TestCatalogExternalOwnerLoadsAndRestoresSourcePins(t *testing.T) {
	cfg := catalogExternalConfig(t)
	store, err := persistence.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if store != nil {
			_ = store.Close()
		}
	})
	_, err = store.CommitAuthentication(t.Context(), persistence.AuthenticationCommand{Mode: "bootstrap", Epoch: uuid.NewString(), Revision: uuid.NewString(), Actor: "local-administrator", At: time.Now().UTC(), Principals: []persistence.AuthenticationPrincipal{{ID: "team/operator", Role: "operator", TokenSHA256: strings.Repeat("12", 32)}}})
	if err != nil {
		t.Fatal(err)
	}
	wrapper, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{83}, 32))
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := secureconfig.NewSealer(wrapper)
	if err != nil {
		t.Fatal(err)
	}
	newCatalog := func() *management.Catalog {
		c, err := management.NewCatalog(store, sealer)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Verify(t.Context()); err != nil {
			t.Fatal(err)
		}
		return c
	}
	catalog := newCatalog()
	types := make(map[string]persistence.JobTypeReference)
	drivers := make(map[string]api.DriverConfig)
	for _, category := range []string{"check", "recovery", "notification"} {
		j := api.JobType{APIVersion: api.APIVersion, Kind: "JobType", Metadata: api.Metadata{ID: "custom-" + category}, Spec: api.JobTypeSpec{Kind: category, Version: "1", Handler: "custom-" + category, ProtocolVersion: "1", Timeout: "5s", ParameterSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["target"],"properties":{"target":{"type":"string"}}}`), ResultSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`)}}
		prepared, err := catalog.PrepareJobType(t.Context(), j, "", true, "team/operator")
		if err != nil {
			t.Fatal(err)
		}
		j, err = catalog.CommitJobType(t.Context(), prepared)
		if err != nil {
			t.Fatal(err)
		}
		types[category] = persistence.JobTypeReference{JobTypeID: j.Metadata.ID, JobTypeUID: j.Metadata.UID, Revision: j.Metadata.ResourceVersion, Version: "1", Category: category}
		drivers[category] = api.DriverConfig{Type: "external", Config: json.RawMessage(`{"jobTypeID":"custom-` + category + `","version":"1","parameters":{"target":"private-parameter-canary"},"credentialProfile":"local-profile-canary"}`)}
	}
	resource := func(kind, id string, spec any) api.Resource {
		raw, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		return api.Resource{APIVersion: api.APIVersion, Kind: kind, Metadata: api.Metadata{ID: id}, Spec: raw}
	}
	var calls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer provider.Close()
	localConfig, _ := json.Marshal(map[string]string{"url": provider.URL})
	commitCatalogExternalFixture(t, store, sealer, resource("NotificationEndpoint", "local", api.DriverConfig{Type: "webhook", Config: localConfig}), nil, nil)
	remoteInput := resource("NotificationEndpoint", "remote-endpoint", drivers["notification"])
	remote := commitCatalogExternalFixture(t, store, sealer, remoteInput, nil, []persistence.JobTypeReference{types["notification"]})
	notifications := map[string]api.AlertRule{
		"red":    {EndpointRefs: api.Pointer([]string{"remote-endpoint", "local"})},
		"yellow": {EndpointRefs: api.Pointer([]string{"local", "remote-endpoint"})},
		"green":  {Driver: api.Pointer(drivers["notification"])},
		"gray":   {Driver: api.Pointer(drivers["notification"]), Dispatch: api.Pointer(false)},
	}
	input := resource("Monitor", "remote", api.MonitorSpec{Enabled: api.Pointer(true), Check: api.CheckSpec{Driver: drivers["check"], Interval: "60s", Timeout: "5s", UnhealthyThreshold: api.Pointer(int64(1))}, Recovery: &api.RecoverySpec{Driver: drivers["recovery"]}, Notifications: &notifications})
	monitor := commitCatalogExternalFixture(t, store, sealer, input, []persistence.CatalogKey{{Kind: "NotificationEndpoint", ID: "remote-endpoint"}, {Kind: "NotificationEndpoint", ID: "local"}}, []persistence.JobTypeReference{types["check"], types["recovery"], types["notification"]})
	wantSources := map[string][]persistence.WorkerNotificationSource{
		"red":    {{Kind: "NotificationEndpoint", ID: "remote-endpoint", UID: remote.Metadata.UID, Revision: remote.Metadata.ResourceVersion}, {}},
		"yellow": {{}, {Kind: "NotificationEndpoint", ID: "remote-endpoint", UID: remote.Metadata.UID, Revision: remote.Metadata.ResourceVersion}},
		"green":  {{Kind: "Monitor", ID: "remote", UID: monitor.Metadata.UID, Revision: monitor.Metadata.ResourceVersion}},
	}
	wantDrivers := map[string][]string{
		"red": {"external", "webhook"}, "yellow": {"webhook", "external"}, "green": {"external"},
	}
	load := func(catalog *management.Catalog) *DurableSystem {
		w := ecs.NewWorld()
		s := NewDurableSystem(&w, store, cfg, &fleetview.Holder{}, noopLogger{}, &admissionQueue{full: true}, nil, nil, nil, time.Minute, false)
		if err := s.LoadCatalog(t.Context(), catalog, entities.NewEntityManager(&w)); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.StopAdmission) // Initialize is deliberately not run: no dispatch goroutine starts.
		m, ok := store.Get("remote")
		if !ok || !reflect.DeepEqual(m.Policy.WorkerNotificationSources, wantSources) || !reflect.DeepEqual(m.Policy.Endpoints, map[string]int{"red": 2, "yellow": 2, "green": 1}) {
			t.Fatal("committed policy changed notification positions or source identities")
		}
		if !reflect.DeepEqual(m.Policy.NotificationDrivers, wantDrivers) {
			t.Fatal("committed policy lost notification driver positions", m.Policy.NotificationDrivers)
		}
		raw, err := json.Marshal(m)
		if err != nil || bytes.Contains(raw, []byte("private-parameter-canary")) || bytes.Contains(raw, []byte("local-profile-canary")) {
			t.Fatal("worker parameters entered the monitor's durable metadata")
		}
		ent := s.entities["remote"]
		jobs := s.jobs.Get(ent)
		if jobs.PulseJob != nil || jobs.InterventionJob != nil || jobs.ExternalJobs == nil || jobs.ExternalJobs.Check == nil || jobs.ExternalJobs.Recovery == nil || jobs.CodeJobs["red"][0] != nil || jobs.CodeJobs["red"][1] == nil || jobs.CodeJobs["yellow"][0] == nil || jobs.CodeJobs["yellow"][1] != nil || s.PulseArrivalRate() != 0 {
			t.Fatal("owner lost external bindings or routed them to local workers")
		}
		for color, drivers := range wantDrivers {
			for index, driver := range drivers {
				flow := jobs.CodeFlows[color][index]
				if flow.Driver != driver || driver == "external" && (flow.Key != 0 || jobs.CodeJobs[color][index] != nil) {
					t.Fatal("external notification gained a local queue or lost its driver", color, index)
				}
			}
		}
		return s
	}
	s := load(catalog)
	s.StopAdmission()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = persistence.Open(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	catalog = newCatalog()
	s = load(catalog)
	view, err := catalog.SnapshotContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	old, err := s.catalogRuntime.prepare(t.Context(), view, "remote")
	if err != nil {
		t.Fatal(err)
	}
	defer old.prepared.Close()
	old.done = make(chan error, 1)
	commitCatalogExternalFixture(t, store, sealer, remoteInput, nil, []persistence.JobTypeReference{types["notification"]})
	s.startCatalogProjection(old)
	assertProjectionAck(t, old, persistence.ErrCatalogDependency)
	input.Metadata.ID = "not-admitted"
	if _, err := catalog.Prepare(t.Context(), input, "", true); !errors.Is(err, management.ErrValidation) {
		t.Fatal("projection support opened public external configuration", err)
	}
	if calls.Load() != 0 {
		t.Fatal("configuration, restoration, or guard rejection invoked a provider")
	}
}
