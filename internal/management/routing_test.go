package management

import (
	"encoding/json"
	"maps"
	"reflect"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func routingCatalog() NotificationCatalog {
	c := NotificationCatalog{Endpoints: map[string]api.NotificationEndpoint{}, Recipients: map[string]api.Recipient{}, Groups: map[string]api.NotificationGroup{}}
	for id, kind := range map[string]string{"mail-a": "email", "mail-b": "email", "sms": "twilio", "chat": "telegram"} {
		c.Endpoints[id] = api.NotificationEndpoint{APIVersion: api.APIVersion, Kind: "NotificationEndpoint", Metadata: api.Metadata{ID: id, UID: id + "-uid", ResourceVersion: "rv-1"}, Spec: api.DriverConfig{Type: kind, Config: json.RawMessage(`{}`)}}
	}
	c.Recipients["alice"] = api.Recipient{APIVersion: api.APIVersion, Kind: "Recipient", Metadata: api.Metadata{ID: "alice", UID: "alice-uid", ResourceVersion: "rv-1"}, Spec: api.RecipientSpec{EndpointRefs: []string{"sms", "mail-b", "mail-a"}}}
	c.Recipients["bob"] = api.Recipient{APIVersion: api.APIVersion, Kind: "Recipient", Metadata: api.Metadata{ID: "bob", UID: "bob-uid", ResourceVersion: "rv-1"}, Spec: api.RecipientSpec{EndpointRefs: []string{"mail-a"}}}
	c.Groups["ops"] = api.NotificationGroup{APIVersion: api.APIVersion, Kind: "NotificationGroup", Metadata: api.Metadata{ID: "ops", UID: "ops-uid", ResourceVersion: "rv-1"}, Spec: api.NotificationGroupSpec{EndpointRefs: []string{"chat", "mail-a"}, RecipientRefs: []string{"alice", "bob"}}}
	c.Groups["legacy"] = api.NotificationGroup{APIVersion: api.APIVersion, Kind: "NotificationGroup", Metadata: api.Metadata{ID: "legacy", UID: "legacy-uid", ResourceVersion: "rv-1"}, Spec: api.NotificationGroupSpec{EndpointRefs: []string{"sms", "mail-a", "chat"}}}
	return c
}

func TestResolveNotificationRule(t *testing.T) {
	for _, tc := range []struct {
		name string
		rule api.AlertRule
		want []string
		err  string
	}{
		{name: "all matching contact methods", rule: api.AlertRule{NotifyType: api.Pointer("email"), RecipientRefs: api.Pointer([]string{"alice"})}, want: []string{"mail-b", "mail-a"}},
		{name: "same contact alternate driver", rule: api.AlertRule{NotifyType: api.Pointer("twilio"), RecipientRefs: api.Pointer([]string{"alice"})}, want: []string{"sms"}},
		{name: "overlap preserves first occurrence", rule: api.AlertRule{NotifyType: api.Pointer("email"), RecipientRefs: api.Pointer([]string{"alice"}), GroupRef: api.Pointer("ops")}, want: []string{"mail-b", "mail-a"}},
		{name: "group filters direct then contacts", rule: api.AlertRule{NotifyType: api.Pointer("email"), GroupRef: api.Pointer("ops")}, want: []string{"mail-a", "mail-b"}},
		{name: "every group person must match", rule: api.AlertRule{NotifyType: api.Pointer("twilio"), GroupRef: api.Pointer("ops")}, err: `recipient "bob" has no endpoint`},
		{name: "direct person cannot be masked by valid group", rule: api.AlertRule{NotifyType: api.Pointer("twilio"), RecipientRefs: api.Pointer([]string{"bob"}), GroupRef: api.Pointer("legacy")}, err: `recipient "bob" has no endpoint`},
		{name: "legacy heterogeneous group", rule: api.AlertRule{GroupRef: api.Pointer("legacy")}, want: []string{"sms", "mail-a", "chat"}},
		{name: "legacy group ignores former notify driver", rule: api.AlertRule{GroupRef: api.Pointer("legacy"), Driver: &api.DriverConfig{Type: "log", Config: json.RawMessage(`{"file":"ignored"}`)}}, want: []string{"sms", "mail-a", "chat"}},
		{name: "legacy recipients require type", rule: api.AlertRule{GroupRef: api.Pointer("ops")}, err: "select Code notification type"},
		{name: "direct recipients require type", rule: api.AlertRule{RecipientRefs: api.Pointer([]string{"alice"})}, err: "select Code notification type"},
		{name: "typed direct endpoint mixture", rule: api.AlertRule{NotifyType: api.Pointer("email"), EndpointRefs: api.Pointer([]string{"mail-a"}), GroupRef: api.Pointer("legacy")}, err: "cannot combine"},
		{name: "typed inline mixture", rule: api.AlertRule{NotifyType: api.Pointer("email"), Driver: &api.DriverConfig{Type: "email", Config: json.RawMessage(`{}`)}, GroupRef: api.Pointer("legacy")}, err: "cannot combine"},
		{name: "duplicate direct recipient", rule: api.AlertRule{NotifyType: api.Pointer("email"), RecipientRefs: api.Pointer([]string{"alice", "alice"})}, err: "duplicate recipient"},
		{name: "no matching group endpoints", rule: api.AlertRule{NotifyType: api.Pointer("slack"), GroupRef: api.Pointer("legacy")}, err: "resolves to no notification endpoints"},
		{name: "missing recipient", rule: api.AlertRule{NotifyType: api.Pointer("email"), RecipientRefs: api.Pointer([]string{"missing"})}, err: "does not exist"},
		{name: "missing group", rule: api.AlertRule{GroupRef: api.Pointer("missing")}, err: "does not exist"},
		{name: "unknown type", rule: api.AlertRule{NotifyType: api.Pointer("sms"), GroupRef: api.Pointer("legacy")}, err: "invalid Code notifyType"},
		{name: "no typed target", rule: api.AlertRule{NotifyType: api.Pointer("email")}, err: "requires a recipient"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalog := routingCatalog()
			before, _ := json.Marshal(catalog)
			result, err := ResolveNotificationRule(tc.rule, catalog)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("wanted %q, got %v", tc.err, err)
				}
				if len(result.Targets) > 0 || result.InlineDriver != nil {
					t.Fatal("returned partial destinations on failure")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			ids := make([]string, 0, len(result.Targets))
			for _, target := range result.Targets {
				ids = append(ids, target.EndpointID)
				if target.EndpointUID != target.EndpointID+"-uid" || target.ResourceVersion != "rv-1" {
					t.Fatalf("lost target identity %+v", target)
				}
			}
			if !reflect.DeepEqual(ids, tc.want) {
				t.Fatalf("got %v want %v", ids, tc.want)
			}
			if result.InlineDriver != nil {
				t.Fatal("group/contact routing unexpectedly returned inline driver")
			}
			after, _ := json.Marshal(catalog)
			if string(before) != string(after) {
				t.Fatal("routing modified catalog")
			}
		})
	}
}

func TestRoutingPreservesFilteredDependenciesAndIncarnations(t *testing.T) {
	catalog := routingCatalog()
	rule := api.AlertRule{NotifyType: api.Pointer("email"), RecipientRefs: api.Pointer([]string{"alice"})}
	result, err := ResolveNotificationRule(rule, catalog)
	if err != nil {
		t.Fatal(err)
	}
	want := []NotificationReference{{Kind: "Recipient", ID: "alice", UID: "alice-uid", ResourceVersion: "rv-1"}, {Kind: "NotificationEndpoint", ID: "sms", UID: "sms-uid", ResourceVersion: "rv-1"}, {Kind: "NotificationEndpoint", ID: "mail-b", UID: "mail-b-uid", ResourceVersion: "rv-1"}, {Kind: "NotificationEndpoint", ID: "mail-a", UID: "mail-a-uid", ResourceVersion: "rv-1"}}
	if !reflect.DeepEqual(result.Dependencies, want) {
		t.Fatalf("lost conditional dependency: %+v", result.Dependencies)
	}
	replacement := catalog.Endpoints["mail-b"]
	replacement.Metadata.UID = "new-incarnation"
	replacement.Metadata.ResourceVersion = "rv-2"
	catalog.Endpoints["mail-b"] = replacement
	next, err := ResolveNotificationRule(rule, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if result.Targets[0].EndpointUID != "mail-b-uid" || next.Targets[0].EndpointUID != "new-incarnation" {
		t.Fatal("incarnation was lost or prior resolution changed")
	}
	// Identical provider configuration does not merge distinct endpoint resources.
	if len(next.Targets) != 2 {
		t.Fatal("distinct endpoint resources were merged")
	}
}

func TestRoutingInlineResultDoesNotAliasCredentials(t *testing.T) {
	refs := map[string]string{"password": "secret-ref"}
	rule := api.AlertRule{Driver: &api.DriverConfig{Type: "email", Config: json.RawMessage(`{}`), CredentialRefs: &refs}}
	result, err := ResolveNotificationRule(rule, NotificationCatalog{})
	if err != nil {
		t.Fatal(err)
	}
	result.InlineDriver.Config[0] = '['
	(*result.InlineDriver.CredentialRefs)["password"] = "changed"
	if string(rule.Driver.Config) != "{}" || refs["password"] != "secret-ref" {
		t.Fatal("inline result aliases input")
	}
}

func TestValidateNotificationGraphRevalidatesAffectedRules(t *testing.T) {
	original := routingCatalog()
	monitors := []api.Monitor{{Metadata: api.Metadata{ID: "payments"}, Spec: api.MonitorSpec{Notifications: api.Pointer(map[string]api.AlertRule{"red": {NotifyType: api.Pointer("email"), RecipientRefs: api.Pointer([]string{"bob"})}})}}}
	if err := ValidateNotificationGraph(original, monitors); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"type", "delete-endpoint", "delete-recipient", "invalid-unused-group"} {
		t.Run(change, func(t *testing.T) {
			candidate := NotificationCatalog{Endpoints: maps.Clone(original.Endpoints), Recipients: maps.Clone(original.Recipients), Groups: maps.Clone(original.Groups)}
			switch change {
			case "type":
				ep := candidate.Endpoints["mail-a"]
				ep.Spec.Type = "twilio"
				candidate.Endpoints["mail-a"] = ep
			case "delete-endpoint":
				delete(candidate.Endpoints, "mail-a")
			case "delete-recipient":
				delete(candidate.Recipients, "bob")
			case "invalid-unused-group":
				candidate.Groups["unused"] = api.NotificationGroup{APIVersion: api.APIVersion, Kind: "NotificationGroup", Metadata: api.Metadata{ID: "unused"}, Spec: api.NotificationGroupSpec{RecipientRefs: []string{"missing"}}}
			}
			if err := ValidateNotificationGraph(candidate, monitors); err == nil {
				t.Fatal("accepted invalid candidate graph")
			}
		})
	}
}
