package management

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// ProtectedDriverFields names fields which must be represented by credential
// references in the management catalog. Protecting complete URL/header/body
// fields avoids turning a redacted read into an unsafe replacement document.
// Legacy import extracts these fields before catalog activation.
func ProtectedDriverFields(category, driver string) []string {
	return slices.Clone(protectedDriverFields[category+"/"+driver])
}

var protectedDriverFields = map[string][]string{
	"check/http":  {"headers", "body"},
	"check/redis": {"password"}, "check/postgres": {"dsn", "password"},
	"check/mysql": {"dsn", "password"}, "check/mongo": {"uri"}, "check/rabbitmq": {"url"},
	"recovery/webhook":   {"url", "headers", "body"},
	"notification/email": {"server"}, "notification/webhook": {"url", "headers"},
	"notification/pagerduty": {"routingKey", "url"}, "notification/slack": {"hook"},
	"notification/telegram": {"botToken", "url"}, "notification/discord": {"webhookUrl"},
	"notification/teams": {"webhookUrl"}, "notification/opsgenie": {"apiKey", "url"},
	"notification/mattermost": {"webhookUrl"}, "notification/pushover": {"appToken", "userKey", "url"},
	"notification/twilio":    {"accountSid", "authToken", "url"},
	"notification/datadog":   {"apiKey", "appKey", "url"},
	"notification/victorops": {"restEndpointKey", "routingKey", "url"},
}

func validateProtectedDriver(category string, driver api.DriverConfig) error {
	if err := validateDriverCredentialScope(driver); err != nil {
		return err
	}
	if err := api.ValidateDriver(category, driver); err != nil {
		return errors.New("invalid or unsupported driver configuration")
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal(driver.Config, &config); err != nil || config == nil {
		return errors.New("driver configuration must be an object")
	}
	for _, field := range ProtectedDriverFields(category, driver.Type) {
		if value, exists := config[field]; exists && !bytes.Equal(value, []byte("null")) {
			return fmt.Errorf("config.%s must use a credentialRefs reference", field)
		}
	}
	if driver.CredentialRefs != nil {
		for field, id := range *driver.CredentialRefs {
			if !validID(id) || field == "" {
				return errors.New("invalid credential reference")
			}
			if value, exists := config[field]; exists && !bytes.Equal(value, []byte("null")) {
				return errors.New("credential reference conflicts with an inline configuration field")
			}
			// Unknown slots must never be injected into an execution driver. Null
			// preserves schema typing while checking the exact configured field.
			probe, _ := json.Marshal(map[string]any{field: nil})
			if err := api.ValidateDriver(category, api.DriverConfig{Type: driver.Type, Config: probe}); err != nil {
				return errors.New("credential reference names an unsupported driver field")
			}
		}
	}
	// A plain HTTP check target can be shown, but URLs with credentials or
	// queries require an explicit protected reference. Error text never echoes it.
	if category == "check" && driver.Type == "http" {
		if raw := config["url"]; len(raw) != 0 && !bytes.Equal(raw, []byte("null")) {
			var target string
			if json.Unmarshal(raw, &target) != nil {
				return errors.New("invalid HTTP check URL")
			}
			u, err := url.Parse(target)
			if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				return errors.New("HTTP URL with credentials, query, or fragment must use credentialRefs.url")
			}
		}
	}
	return nil
}

func validID(id string) bool {
	return id != "" && len(id) <= 256 && id != "." && id != ".." &&
		!strings.ContainsAny(id, "/\\?#%\x00\r\n")
}

// visitDrivers visits only supported executable driver envelopes. It neither
// constructs clients nor invokes a provider. Changes are applied to a local DTO.
func visitDrivers(resource *api.Resource, visit func(string, *api.DriverConfig) error) error {
	switch resource.Kind {
	case "NotificationEndpoint":
		var driver api.DriverConfig
		if err := api.StrictDecode(resource.Spec, &driver); err != nil {
			return errors.New("invalid endpoint spec")
		}
		if err := visit("notification", &driver); err != nil {
			return err
		}
		resource.Spec, _ = json.Marshal(driver)
	case "Monitor":
		var spec api.MonitorSpec
		if err := api.StrictDecode(resource.Spec, &spec); err != nil {
			return errors.New("invalid monitor spec")
		}
		if err := visit("check", &spec.Check.Driver); err != nil {
			return err
		}
		if spec.Recovery != nil {
			if err := visit("recovery", &spec.Recovery.Driver); err != nil {
				return err
			}
		}
		if spec.Notifications != nil {
			for color, rule := range *spec.Notifications {
				if rule.Driver != nil {
					if err := visit("notification", rule.Driver); err != nil {
						return err
					}
					(*spec.Notifications)[color] = rule
				}
			}
		}
		resource.Spec, _ = json.Marshal(spec)
	}
	return nil
}
