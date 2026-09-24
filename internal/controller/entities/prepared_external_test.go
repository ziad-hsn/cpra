//go:build externaljobs

package entities

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/manifest"
)

func externalTestBinding(t *testing.T, kind, id, slot, category string) manifest.ExternalRuntimeBinding {
	t.Helper()
	identity := manifest.ExternalJobIdentity{JobTypeID: "custom-" + category, JobTypeUID: "type-uid-" + category, Version: "v1", Revision: "type-revision-" + category, Category: category}
	descriptor, err := manifest.NewExternalJobDescriptor(identity, []byte(`{"value":"parameter-canary"}`), "credential-profile-canary")
	if err != nil {
		t.Fatal(err)
	}
	return manifest.ExternalRuntimeBinding{Key: manifest.ExternalRuntimeKey{SourceKind: kind, SourceID: id, SourceUID: "uid-" + id, SourceRevision: "revision-" + id, Slot: slot, JobType: identity}, Descriptor: descriptor}
}

func externalTestMonitor(t *testing.T) (manifest.Monitor, map[string]manifest.Endpoint, manifest.NotificationGroups, []manifest.ExternalRuntimeBinding) {
	t.Helper()
	m := testMonitor()
	bindings := []manifest.ExternalRuntimeBinding{
		externalTestBinding(t, "Monitor", m.ID, "check", "check"),
		externalTestBinding(t, "Monitor", m.ID, "recovery", "recovery"),
		externalTestBinding(t, "NotificationEndpoint", "first", "endpoint", "notification"),
		externalTestBinding(t, "NotificationEndpoint", "last", "endpoint", "notification"),
		externalTestBinding(t, "Monitor", m.ID, "notifications.green", "notification"),
	}
	m.Pulse.Type, m.Pulse.Config = "external", &manifest.ExternalPulseConfig{Key: bindings[0].Key}
	m.Intervention = manifest.Intervention{Action: "external", Target: &manifest.ExternalInterventionConfig{Key: bindings[1].Key}}
	m.Codes = manifest.Codes{
		"red":    {Dispatch: true, NotifyGroup: "mixed"},
		"yellow": {Dispatch: true, NotifyGroup: "mixed"},
		"green":  {Dispatch: true, Notify: "external", Config: &manifest.ExternalNotificationConfig{Key: bindings[4].Key}},
	}
	endpoints := map[string]manifest.Endpoint{
		"first": {Type: "external", Config: &manifest.ExternalNotificationConfig{Key: bindings[2].Key}},
		"local": {Type: "webhook", Config: &manifest.CodeNotificationWebhook{URL: "http://127.0.0.1:1", Headers: map[string]string{}}},
		"last":  {Type: "external", Config: &manifest.ExternalNotificationConfig{Key: bindings[3].Key}},
	}
	return m, endpoints, manifest.NotificationGroups{"mixed": {"first", "local", "last", "first"}}, bindings
}

func TestPrepareExternalBindingsPreserveOrdinalsAndOwnership(t *testing.T) {
	var invocations atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { invocations.Add(1) }))
	defer provider.Close()
	m, endpoints, groups, bindings := externalTestMonitor(t)
	endpoints["local"].Config.(*manifest.CodeNotificationWebhook).URL = provider.URL
	wantRevision, err := manifest.ConfigurationRevision(m, endpoints, groups)
	if err != nil {
		t.Fatal(err)
	}
	p, err := PrepareMonitorExternal(m, endpoints, groups, bindings)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.Revision() != wantRevision {
		t.Fatal("preparation changed the original identity fingerprint")
	}
	// All caller-owned containers can be changed before the world transfer.
	bindings[0].Key.SourceUID = "changed"
	bindings[0].Descriptor = manifest.ExternalJobDescriptor{}
	m.Pulse.Config.(*manifest.ExternalPulseConfig).Key.SourceRevision = "changed"
	endpoints["first"].Config.(*manifest.ExternalNotificationConfig).Key.SourceUID = "changed"
	groups["mixed"][0] = "missing"
	w := ecs.NewWorld()
	manager := NewEntityManager(&w)
	entity, err := manager.Install(p, &w)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	storage := manager.JobStorage.Get(entity)
	if storage.PulseJob != nil || storage.InterventionJob != nil || storage.ExternalJobs == nil {
		t.Fatal("external descriptors became local jobs or were discarded")
	}
	if storage.ExternalJobs.Check.Key.SourceUID != "uid-stable-api" || storage.ExternalJobs.Recovery.Key.Slot != "recovery" {
		t.Fatal("preparation retained borrowed source identity")
	}
	if string(storage.ExternalJobs.Check.Descriptor.Parameters()) != `{"value":"parameter-canary"}` {
		t.Fatal("preparation retained borrowed descriptor state")
	}
	for _, color := range []string{"red", "yellow"} {
		local, external := storage.CodeJobs[color], storage.ExternalJobs.Notifications[color]
		if len(local) != 4 || len(external) != 4 || local[1] == nil || local[1].IsNil() || external[1] != nil {
			t.Fatal("mixed endpoint arrays lost local ordinal 1")
		}
		for _, ordinal := range []int{0, 2, 3} {
			if local[ordinal] != nil || external[ordinal] == nil {
				t.Fatal("external endpoint acquired a local executable job")
			}
		}
		if external[0].Key.SourceID != "first" || external[2].Key.SourceID != "last" || external[3] != external[0] {
			t.Fatal("notification ordering or repeated binding ownership changed")
		}
	}
	if storage.ExternalJobs.Notifications["red"][0] != storage.ExternalJobs.Notifications["yellow"][0] {
		t.Fatal("shared destination allocated another private descriptor")
	}
	if len(storage.CodeJobs["green"]) != 1 || storage.CodeJobs["green"][0] != nil || storage.ExternalJobs.Notifications["green"][0].Key.SourceKind != "Monitor" {
		t.Fatal("inert inline identity was not preserved")
	}
	cloned := storage.Copy()
	if cloned.ExternalJobs == storage.ExternalJobs || cloned.ExternalJobs.Check == storage.ExternalJobs.Check || cloned.ExternalJobs.Notifications["red"][0] == storage.ExternalJobs.Notifications["red"][0] {
		t.Fatal("storage copy borrowed external bindings")
	}
	if cloned.ExternalJobs.Notifications["red"][0] != cloned.ExternalJobs.Notifications["yellow"][3] {
		t.Fatal("copy duplicated shared parameter buffers")
	}
	storage.ExternalJobs.Notifications["red"][0].Key.SourceUID = "mutated installed input"
	if cloned.ExternalJobs.Notifications["red"][0].Key.SourceUID != "uid-first" || len(cloned.CodeJobs["red"]) != 4 || cloned.CodeJobs["red"][0] != nil || cloned.CodeJobs["red"][1] == nil {
		t.Fatal("copy borrowed an identity or compacted local positions")
	}
	parameters := cloned.ExternalJobs.Check.Descriptor.Parameters()
	clear(parameters)
	if !bytes.Contains(cloned.ExternalJobs.Check.Descriptor.Parameters(), []byte("parameter-canary")) {
		t.Fatal("descriptor accessor exposed owned parameter buffer")
	}
	for _, value := range []any{p, storage.ExternalJobs, *storage.ExternalJobs, cloned} {
		for _, verb := range []string{"%v", "%+v", "%#v"} {
			formatted := fmt.Sprintf(verb, value)
			if strings.Contains(formatted, "parameter-canary") || strings.Contains(formatted, "credential-profile-canary") {
				t.Fatal("formatting exposed external configuration")
			}
		}
	}
	if _, err := json.Marshal(storage.ExternalJobs); err == nil {
		t.Fatal("external configuration was serializable")
	}
	if invocations.Load() != 0 {
		t.Fatal("preparation, installation, or copying invoked a provider")
	}
}

func TestPrepareExternalBindingsRejectMismatchedInputs(t *testing.T) {
	for _, name := range []string{"missing", "duplicate", "unused", "descriptor identity", "marker identity", "source incarnation", "endpoint identity", "wrong category", "driver selector", "ambiguous inline", "binding count", "binding bytes"} {
		t.Run(name, func(t *testing.T) {
			m, endpoints, groups, bindings := externalTestMonitor(t)
			switch name {
			case "missing":
				bindings = bindings[1:]
			case "duplicate":
				bindings = append(bindings, bindings[0])
			case "unused":
				bindings = append(bindings, externalTestBinding(t, "NotificationEndpoint", "unused", "endpoint", "notification"))
			case "descriptor identity":
				bindings[0].Descriptor = bindings[1].Descriptor
			case "marker identity":
				m.Pulse.Config.(*manifest.ExternalPulseConfig).Key.JobType.Revision = "different"
			case "source incarnation":
				bindings[0].Key.SourceUID = "different"
				m.Pulse.Config.(*manifest.ExternalPulseConfig).Key = bindings[0].Key
			case "endpoint identity":
				bindings[2].Key.SourceID = "other-endpoint"
				endpoints["first"].Config.(*manifest.ExternalNotificationConfig).Key = bindings[2].Key
			case "wrong category":
				bindings[0].Key.JobType.Category = "recovery"
			case "driver selector":
				m.Pulse.Type = "http"
			case "ambiguous inline":
				code := m.Codes["red"]
				code.Config = &manifest.ExternalNotificationConfig{Key: bindings[4].Key}
				m.Codes["red"] = code
			case "binding count":
				bindings = make([]manifest.ExternalRuntimeBinding, maxExternalPreparationBindings+1)
			case "binding bytes":
				bindings = make([]manifest.ExternalRuntimeBinding, 260)
				for i := range bindings {
					b := externalTestBinding(t, "NotificationEndpoint", fmt.Sprintf("endpoint-%d", i), "endpoint", "notification")
					var err error
					b.Descriptor, err = manifest.NewExternalJobDescriptor(b.Key.JobType, []byte(`"`+strings.Repeat("x", (128<<10)-2)+`"`), "")
					if err != nil {
						t.Fatal(err)
					}
					bindings[i] = b
				}
			}
			if prepared, err := PrepareMonitorExternal(m, endpoints, groups, bindings); err == nil || prepared != nil {
				t.Fatal("invalid external preparation accepted")
			}
		})
	}
}

func TestPrepareExternalMarkersRequireExplicitSidecar(t *testing.T) {
	for _, placement := range []string{"check", "recovery", "inline", "endpoint", "ignored inline"} {
		t.Run(placement, func(t *testing.T) {
			m := testMonitor()
			var endpoints map[string]manifest.Endpoint
			var groups manifest.NotificationGroups
			switch placement {
			case "check":
				b := externalTestBinding(t, "Monitor", m.ID, "check", "check")
				m.Pulse.Type, m.Pulse.Config = "external", &manifest.ExternalPulseConfig{Key: b.Key}
			case "recovery":
				b := externalTestBinding(t, "Monitor", m.ID, "recovery", "recovery")
				m.Intervention = manifest.Intervention{Action: "external", Target: &manifest.ExternalInterventionConfig{Key: b.Key}}
			case "inline", "ignored inline":
				b := externalTestBinding(t, "Monitor", m.ID, "notifications.red", "notification")
				m.Codes = manifest.Codes{"red": {Notify: "external", Config: &manifest.ExternalNotificationConfig{Key: b.Key}}}
				if placement == "ignored inline" {
					code := m.Codes["red"]
					code.NotifyGroup = "local"
					m.Codes["red"] = code
					groups = manifest.NotificationGroups{"local": {"local"}}
					endpoints = map[string]manifest.Endpoint{"local": {Type: "log", Config: &manifest.CodeNotificationLog{}}}
				}
			case "endpoint":
				b := externalTestBinding(t, "NotificationEndpoint", "remote", "endpoint", "notification")
				m.Codes = manifest.Codes{"red": {NotifyGroup: "remote"}}
				groups = manifest.NotificationGroups{"remote": {"remote"}}
				endpoints = map[string]manifest.Endpoint{"remote": {Type: "external", Config: &manifest.ExternalNotificationConfig{Key: b.Key}}}
			}
			if p, err := PrepareMonitor(m, endpoints, groups); err == nil || p != nil {
				t.Fatal("ordinary preparation enabled an external marker")
			}
		})
	}
}
