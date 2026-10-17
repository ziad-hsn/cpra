package management

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func TestNotificationLogAdmissionCapability(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		options := CatalogOptions{NotificationLogEnabled: enabled}
		for _, test := range []struct {
			config string
			refs   *map[string]string
			file   bool
		}{
			{`{}`, nil, false}, {`{"file":""}`, nil, false},
			{`{"file":"team/alerts.jsonl"}`, nil, true},
			{`{}`, api.Pointer(map[string]string{"file": "destination"}), true},
		} {
			driver := api.DriverConfig{Type: "log", Config: json.RawMessage(test.config), CredentialRefs: test.refs}
			want := enabled || !test.file
			for _, err := range []error{options.validateDriver("notification", driver), options.startupCapabilities(resource("NotificationEndpoint", "log", driver)), (&Stage{options: options}).validateResolvedDriver(t.Context(), "notification", api.DriverConfig{Type: "log", Config: driver.Config})} {
				// Staging sees resolved values; the reference-only fixture is tested
				// at desired admission, while a concrete filename covers resolution.
				if test.refs != nil {
					break
				}
				if (err == nil) != want || err != nil && !errors.Is(err, ErrValidation) {
					t.Fatalf("enabled=%t file=%t err=%v", enabled, test.file, err)
				}
			}
			if (options.validateDriver("notification", driver) == nil) != want {
				t.Fatal("credential-backed path bypassed capability")
			}
		}
	}
	_, disabled, err := (CatalogOptions{}).validationProfile()
	if err != nil {
		t.Fatal(err)
	}
	_, enabled, err := (CatalogOptions{NotificationLogEnabled: true}).validationProfile()
	if err != nil {
		t.Fatal(err)
	}
	if disabled == enabled {
		t.Fatal("process capability missing from validation profile")
	}
}

func TestNotificationLogRestartRejectsMissingCapability(t *testing.T) {
	c, _ := testCatalog(t)
	createResource(t, c, resource("NotificationEndpoint", "file", api.DriverConfig{Type: "log", Config: json.RawMessage(`{"file":"alerts.jsonl"}`)}))
	if _, err := StartupCatalog(t.Context(), c.store, c.sealer, StartupOptions{}); err == nil {
		t.Fatal("committed file destination restored without process capability")
	}
}
