//go:build externaljobs

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

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/controller/entities"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func runtimeExternalDriver(category string) api.DriverConfig {
	return api.DriverConfig{Type: "external", Config: json.RawMessage(`{"jobTypeID":"custom-` + category + `","version":"1","parameters":{"target":"private-parameter-canary"},"credentialProfile":"private-profile-canary"}`)}
}
func TestExternalRuntimeBindingsAuthenticateAllSlots(t *testing.T) {
	c, _ := jobTypeCatalog(t)
	for _, category := range []string{"check", "recovery", "notification"} {
		createReferenceJobType(t, c, category)
	}
	notification := runtimeExternalDriver("notification")
	rules := map[string]api.AlertRule{}
	for _, color := range []string{"red", "yellow", "green", "cyan", "gray"} {
		rules[color] = api.AlertRule{Driver: &notification}
	}
	input := resource("Monitor", "monitor", api.MonitorSpec{Check: api.CheckSpec{Driver: runtimeExternalDriver("check"), Interval: "60s", Timeout: "5s"}, Recovery: &api.RecoverySpec{Driver: runtimeExternalDriver("recovery")}, Notifications: &rules})
	prepared := prepareReferenceResource(t, c, input)
	authenticated, err := c.open(t.Context(), prepared.mutation.Record)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := externalRuntimeBindings(t.Context(), authenticated, prepared.mutation.Record)
	if err != nil {
		t.Fatal(err)
	}
	slots := []string{}
	for _, binding := range bindings {
		slots = append(slots, binding.Key.Slot)
		if binding.Key.SourceKind != "Monitor" || binding.Key.SourceID != "monitor" || binding.Key.SourceUID != authenticated.Metadata.UID || binding.Key.SourceRevision != authenticated.Metadata.ResourceVersion || binding.Key.JobType != binding.Descriptor.Identity() {
			t.Fatal("binding changed original identity")
		}
		matched := false
		for _, ref := range prepared.mutation.Record.JobTypeReferences {
			if ref.JobTypeUID == binding.Key.JobType.JobTypeUID && ref.Revision == binding.Key.JobType.Revision && ref.Version == binding.Key.JobType.Version {
				matched = true
			}
		}
		if !matched || binding.Descriptor.CredentialProfile() != "private-profile-canary" || !bytes.Contains(binding.Descriptor.Parameters(), []byte("private-parameter-canary")) {
			t.Fatal("contract or private input lost")
		}
	}
	if !slices.Equal(slots, []string{"check", "recovery", "notifications.cyan", "notifications.gray", "notifications.green", "notifications.red", "notifications.yellow"}) {
		t.Fatal(slots)
	}
	// Read a private descriptor twice after removing the decrypted source bytes.
	clear(authenticated.Spec)
	copy := bindings[0].Descriptor.Parameters()
	clear(copy)
	if !bytes.Contains(bindings[0].Descriptor.Parameters(), []byte("private-parameter-canary")) {
		t.Fatal("descriptor aliases borrowed input")
	}
	// Metadata changes to the current type must not retarget the retained version.
	current, err := c.GetJobType(t.Context(), "custom-check")
	if err != nil {
		t.Fatal(err)
	}
	current = commitTestJobType(t, c, current, current.Metadata.ResourceVersion, false)
	original, err := c.open(t.Context(), prepared.mutation.Record)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := externalRuntimeBindings(t.Context(), original, prepared.mutation.Record)
	if err != nil || repeated[0].Key != bindings[0].Key || repeated[0].Key.JobType.Revision == current.Metadata.ResourceVersion {
		t.Fatal("retained contract rebound to current descriptor", err)
	}
}
func TestExternalRuntimeBindingsKeepEndpointScope(t *testing.T) {
	c, _ := jobTypeCatalog(t)
	createReferenceJobType(t, c, "notification")
	endpoint := prepareReferenceResource(t, c, resource("NotificationEndpoint", "destination", runtimeExternalDriver("notification")))
	opened, err := c.open(t.Context(), endpoint.mutation.Record)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err := externalRuntimeBindings(t.Context(), opened, endpoint.mutation.Record)
	if err != nil || len(bindings) != 1 || bindings[0].Key.SourceKind != "NotificationEndpoint" || bindings[0].Key.SourceID != "destination" || bindings[0].Key.Slot != "endpoint" {
		t.Fatal("endpoint binding lost", err)
	}
	// A selector routes to endpoints; it must not synthesize a monitor-owned job.
	rules := map[string]api.AlertRule{"red": {NotifyType: api.Pointer("external"), GroupRef: api.Pointer("group")}}
	monitor := resource("Monitor", "m", api.MonitorSpec{Check: api.CheckSpec{Driver: api.DriverConfig{Type: "http", Config: json.RawMessage(`{"url":"https://example.test"}`)}, Interval: "60s", Timeout: "5s"}, Notifications: &rules})
	monitor.Metadata.UID, monitor.Metadata.ResourceVersion, monitor.Metadata.Generation = "uid", "revision", 1
	record := persistence.CatalogRecord{Key: persistence.CatalogKey{Kind: "Monitor", ID: "m"}, UID: "uid", Revision: "revision", Generation: 1}
	got, err := externalRuntimeBindings(t.Context(), monitor, record)
	if err != nil || len(got) != 0 {
		t.Fatal("notification selector fabricated direct binding", err)
	}
}
func TestExternalRuntimeBindingsRejectChangedIdentityAndReferences(t *testing.T) {
	c, _ := jobTypeCatalog(t)
	createReferenceJobType(t, c, "check")
	prepared := prepareReferenceResource(t, c, externalReferenceResource("check", false))
	original, err := c.open(t.Context(), prepared.mutation.Record)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*api.Resource, *persistence.CatalogRecord){
		"source uid":        func(r *api.Resource, _ *persistence.CatalogRecord) { r.Metadata.UID = "other" },
		"source revision":   func(r *api.Resource, _ *persistence.CatalogRecord) { r.Metadata.ResourceVersion = "other" },
		"source generation": func(r *api.Resource, _ *persistence.CatalogRecord) { r.Metadata.Generation++ },
		"source kind":       func(r *api.Resource, _ *persistence.CatalogRecord) { r.Kind = "NotificationEndpoint" },
		"removed":           func(_ *api.Resource, r *persistence.CatalogRecord) { r.Removed = true },
		"missing pin":       func(_ *api.Resource, r *persistence.CatalogRecord) { r.JobTypeReferences = nil },
		"wrong version":     func(_ *api.Resource, r *persistence.CatalogRecord) { r.JobTypeReferences[0].Version = "2" },
		"wrong category":    func(_ *api.Resource, r *persistence.CatalogRecord) { r.JobTypeReferences[0].Category = "recovery" },
		"duplicate pin": func(_ *api.Resource, r *persistence.CatalogRecord) {
			r.JobTypeReferences = append(r.JobTypeReferences, r.JobTypeReferences[0])
		},
		"extra pin": func(_ *api.Resource, r *persistence.CatalogRecord) {
			extra := r.JobTypeReferences[0]
			extra.JobTypeID = "other"
			r.JobTypeReferences = append(r.JobTypeReferences, extra)
			r.JobTypeReferences, _ = persistence.CanonicalJobTypeReferences(r.JobTypeReferences)
		},
		"oversized spec": func(r *api.Resource, _ *persistence.CatalogRecord) { r.Spec = bytes.Repeat([]byte(" "), (1<<20)+1) },
		"credential reference": func(r *api.Resource, _ *persistence.CatalogRecord) {
			_ = visitDrivers(r, func(_ string, d *api.DriverConfig) error {
				d.CredentialRefs = api.Pointer(map[string]string{"parameters": "private-secret-canary"})
				return nil
			})
		},
		"invalid color": func(r *api.Resource, _ *persistence.CatalogRecord) {
			var s api.MonitorSpec
			_ = json.Unmarshal(r.Spec, &s)
			s.Notifications = &map[string]api.AlertRule{"blue": {}}
			r.Spec, _ = json.Marshal(s)
		},
	}
	for name, edit := range tests {
		t.Run(name, func(t *testing.T) {
			r := original
			r.Spec = slices.Clone(original.Spec)
			record := prepared.mutation.Record.Clone()
			edit(&r, &record)
			before := record.Clone()
			got, err := externalRuntimeBindings(t.Context(), r, record)
			if !errors.Is(err, ErrValidation) || got != nil || strings.Contains(err.Error(), "private-") || !reflect.DeepEqual(before, record) {
				t.Fatal("invalid binding accepted, leaked, or mutated source", err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := externalRuntimeBindings(ctx, original, prepared.mutation.Record); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := externalRuntimeBindings(nil, original, prepared.mutation.Record); !errors.Is(err, ErrValidation) {
		t.Fatal(err)
	}
}
func TestExternalRuntimeBindingsDoNotEnableExecution(t *testing.T) {
	c, _ := jobTypeCatalog(t)
	for _, category := range []string{"check", "recovery", "notification"} {
		createReferenceJobType(t, c, category)
		input := externalReferenceResource(category, false)
		if _, err := c.Prepare(t.Context(), input, "", true); !errors.Is(err, ErrValidation) {
			t.Fatal("public configuration admission enabled", category, err)
		}
		driver := runtimeExternalDriver(category)
		if ValidateResolvedDriver(category, driver) == nil {
			t.Fatal("ordinary runtime adapter enabled", category)
		}
		if _, err := decodeRuntimeDriver(category, driver); err == nil {
			t.Fatal("ordinary runtime conversion enabled", category)
		}
	}
	// Inert descriptors cannot accidentally satisfy any executable or legacy-config interface.
	descriptor, _ := manifest.NewExternalJobDescriptor(manifest.ExternalJobIdentity{JobTypeID: "custom", JobTypeUID: "uid", Version: "1", Revision: "revision", Category: "check"}, []byte(`{}`), "")
	if _, ok := any(descriptor).(jobs.Job); ok {
		t.Fatal("descriptor executes")
	}
	if _, ok := any(descriptor).(manifest.PulseConfig); ok {
		t.Fatal("descriptor is a local pulse config")
	}
}

func TestExternalRuntimeBindingsPrepareInertEntities(t *testing.T) {
	c, _ := jobTypeCatalog(t)
	for _, category := range []string{"check", "recovery", "notification"} {
		createReferenceJobType(t, c, category)
	}
	input := resource("Monitor", "monitor", api.MonitorSpec{Check: api.CheckSpec{Driver: runtimeExternalDriver("check"), Interval: "60s", Timeout: "5s"}, Recovery: &api.RecoverySpec{Driver: runtimeExternalDriver("recovery")}})
	monitorRecord := prepareReferenceResource(t, c, input).mutation.Record
	endpointRecord := prepareReferenceResource(t, c, resource("NotificationEndpoint", "destination", runtimeExternalDriver("notification"))).mutation.Record
	var bindings []manifest.ExternalRuntimeBinding
	for _, record := range []persistence.CatalogRecord{monitorRecord, endpointRecord} {
		opened, err := c.open(t.Context(), record)
		if err != nil {
			t.Fatal(err)
		}
		selected, err := externalRuntimeBindings(t.Context(), opened, record)
		if err != nil {
			t.Fatal(err)
		}
		bindings = append(bindings, selected...)
	}
	if len(bindings) != 3 {
		t.Fatal("missing runtime bindings")
	}
	monitor := manifest.Monitor{ID: "monitor", Name: "monitor", Enabled: true, Pulse: manifest.Pulse{Type: "external", Config: &manifest.ExternalPulseConfig{Key: bindings[0].Key}}, Intervention: manifest.Intervention{Action: "external", Target: &manifest.ExternalInterventionConfig{Key: bindings[1].Key}}, Codes: manifest.Codes{"red": {Dispatch: true, NotifyGroup: "destinations"}}}
	endpoints := map[string]manifest.Endpoint{"destination": {Type: "external", Config: &manifest.ExternalNotificationConfig{Key: bindings[2].Key}}}
	groups := manifest.NotificationGroups{"destinations": {"destination"}}
	if p, err := entities.PrepareMonitor(monitor, endpoints, groups); err == nil {
		_ = p.Close()
		t.Fatal("generic preparation admitted external markers")
	}
	prepared, err := entities.PrepareMonitorExternal(monitor, endpoints, groups, bindings)
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	world := ecs.NewWorld()
	manager := entities.NewEntityManager(&world)
	entity, err := manager.Install(prepared, &world)
	if err != nil {
		t.Fatal(err)
	}
	storage := manager.JobStorage.Get(entity)
	if storage.PulseJob != nil || storage.InterventionJob != nil || len(storage.CodeJobs["red"]) != 1 || storage.CodeJobs["red"][0] != nil || storage.ExternalJobs == nil {
		t.Fatal("binding became executable local work")
	}
	if storage.ExternalJobs.Check.Key != bindings[0].Key || storage.ExternalJobs.Recovery.Key != bindings[1].Key || storage.ExternalJobs.Notifications["red"][0].Key != bindings[2].Key {
		t.Fatal("entity installation discarded original slot identity")
	}
}
