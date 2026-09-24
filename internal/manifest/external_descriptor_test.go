//go:build externaljobs

package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func testExternalIdentity() ExternalJobIdentity {
	return ExternalJobIdentity{JobTypeID: "custom", JobTypeUID: "type-uid", Version: "1", Revision: "immutable-revision", Category: "check"}
}
func TestExternalDescriptorOwnsParametersAndRedacts(t *testing.T) {
	raw := []byte(`{"target":"private-parameter-canary"}`)
	descriptor, err := NewExternalJobDescriptor(testExternalIdentity(), raw, "private-profile-canary")
	if err != nil {
		t.Fatal(err)
	}
	clear(raw)
	first := descriptor.Parameters()
	if !bytes.Contains(first, []byte("private-parameter-canary")) {
		t.Fatal("input was not detached")
	}
	clear(first)
	clone := descriptor.Clone()
	clear(descriptor.parameters)
	if !bytes.Contains(clone.Parameters(), []byte("private-parameter-canary")) {
		t.Fatal("clone shares private bytes")
	}
	key := ExternalRuntimeKey{SourceKind: "Monitor", SourceID: "monitor", SourceUID: "monitor-uid", SourceRevision: "revision", Slot: "check", JobType: testExternalIdentity()}
	binding := ExternalRuntimeBinding{Key: key, Descriptor: clone}
	for _, v := range []any{clone, &clone, binding, &binding} {
		for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
			s := fmt.Sprintf(format, v)
			if strings.Contains(s, "private-") {
				t.Fatal("format exposed private input", format)
			}
		}
		if raw, err := json.Marshal(v); err == nil || bytes.Contains(raw, []byte("private-")) {
			t.Fatal("JSON serialization allowed")
		}
		if raw, err := yaml.Marshal(v); err == nil || bytes.Contains(raw, []byte("private-")) {
			t.Fatal("YAML serialization allowed")
		}
	}
	if clone.Identity() != testExternalIdentity() || clone.CredentialProfile() != "private-profile-canary" {
		t.Fatal("descriptor identity changed")
	}
}
func TestExternalDescriptorBoundsAndSlotIdentity(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte(`{"x":`), []byte{'"', 255, '"'}, []byte(`"` + strings.Repeat("x", 128<<10) + `"`)} {
		if _, err := NewExternalJobDescriptor(testExternalIdentity(), raw, ""); err == nil {
			t.Fatal("invalid parameters accepted")
		}
	}
	for _, mutate := range []func(*ExternalJobIdentity){func(i *ExternalJobIdentity) { i.JobTypeUID = "" }, func(i *ExternalJobIdentity) { i.Revision = "bad\n" }, func(i *ExternalJobIdentity) { i.Version = "../bad" }, func(i *ExternalJobIdentity) { i.Category = "other" }} {
		i := testExternalIdentity()
		mutate(&i)
		if _, err := NewExternalJobDescriptor(i, []byte(`{}`), ""); err == nil {
			t.Fatal("invalid identity accepted")
		}
	}
	if _, err := NewExternalJobDescriptor(testExternalIdentity(), []byte(`{}`), "../profile"); err == nil {
		t.Fatal("invalid profile accepted")
	}
	for _, tc := range []struct {
		kind, slot, category string
		valid                bool
	}{{"Monitor", "check", "check", true}, {"Monitor", "recovery", "recovery", true}, {"Monitor", "notifications.red", "notification", true}, {"NotificationEndpoint", "endpoint", "notification", true}, {"Monitor", "endpoint", "notification", false}, {"Monitor", "check", "recovery", false}, {"NotificationEndpoint", "check", "check", false}, {"Monitor", "notifications.blue", "notification", false}} {
		key := ExternalRuntimeKey{SourceKind: tc.kind, SourceID: "id", SourceUID: "uid", SourceRevision: "revision", Slot: tc.slot, JobType: testExternalIdentity()}
		key.JobType.Category = tc.category
		if (key.Validate() == nil) != tc.valid {
			t.Fatalf("slot mismatch: %+v", tc)
		}
	}
}
func TestExternalMarkersContainOnlyRevisionIdentity(t *testing.T) {
	key := ExternalRuntimeKey{SourceKind: "Monitor", SourceID: "m", SourceUID: "uid", SourceRevision: "revision1", Slot: "check", JobType: testExternalIdentity()}
	m := Monitor{Name: "monitor", Enabled: true, Pulse: Pulse{Type: "external", Config: &ExternalPulseConfig{Key: key}}}
	first, err := ConfigurationRevision(m, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	copied := m.Pulse.Config.Copy().(*ExternalPulseConfig)
	copied.Key.SourceRevision = "revision2"
	if m.Pulse.Config.(*ExternalPulseConfig).Key.SourceRevision != "revision1" {
		t.Fatal("marker copy aliased")
	}
	m.Pulse.Config = copied
	second, err := ConfigurationRevision(m, nil, nil)
	if err != nil || first == second {
		t.Fatal("source revision did not change runtime fingerprint", err)
	}
	copied.Key.JobType.Revision = "immutable2"
	third, err := ConfigurationRevision(m, nil, nil)
	if err != nil || second == third {
		t.Fatal("type revision did not change runtime fingerprint", err)
	}
	raw, err := json.Marshal(m)
	if err != nil || bytes.Contains(raw, []byte("parameters")) || bytes.Contains(raw, []byte("credentialProfile")) {
		t.Fatal("marker contains private configuration", err)
	}
	var _ PulseConfig = (*ExternalPulseConfig)(nil)
	var _ InterventionTarget = (*ExternalInterventionConfig)(nil)
	var _ CodeNotification = (*ExternalNotificationConfig)(nil)
}
