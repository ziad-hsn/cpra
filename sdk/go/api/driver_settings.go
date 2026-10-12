package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"
)

// ValidateResourceSettings adds current static settings requirements to resource
// shape validation. Parsing and original-input verification keep their versioned
// byte contract; new mutations and preflight validate settings separately.
func ValidateResourceSettings(resource Resource) error {
	if err := ValidateResource(resource); err != nil {
		return err
	}
	switch resource.Kind {
	case "Monitor":
		var spec MonitorSpec
		_ = json.Unmarshal(resource.Spec, &spec)
		if err := ValidateDriverSettings("check", spec.Check.Driver); err != nil {
			return err
		}
		if spec.Recovery != nil {
			if err := ValidateDriverSettings("recovery", spec.Recovery.Driver); err != nil {
				return err
			}
		}
		if spec.Notifications != nil {
			for _, rule := range *spec.Notifications {
				if rule.Driver != nil {
					if err := ValidateDriverSettings("notification", *rule.Driver); err != nil {
						return err
					}
				}
			}
		}
	case "NotificationEndpoint":
		var driver DriverConfig
		_ = json.Unmarshal(resource.Spec, &driver)
		return ValidateDriverSettings("notification", driver)
	}
	return nil
}

// ValidateDriverSettings checks the static configuration contract, including
// required fields. Credential references satisfy presence until the server
// resolves their values and validates again. This performs no provider I/O,
// platform discovery, file access, or executable client construction.
func ValidateDriverSettings(category string, driver DriverConfig) error {
	if err := ValidateDriver(category, driver); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := StrictDecode(driver.Config, &fields); err != nil || fields == nil {
		return errors.New("driver configuration must be an object")
	}
	referenced := func(field string) bool {
		return driver.CredentialRefs != nil && (*driver.CredentialRefs)[field] != ""
	}
	present := func(field string) bool {
		raw := bytes.TrimSpace(fields[field])
		return referenced(field) || len(raw) != 0 && !bytes.Equal(raw, []byte("null")) && !bytes.Equal(raw, []byte(`""`)) && !bytes.Equal(raw, []byte("[]"))
	}
	for _, field := range requiredDriverFields[category+"/"+driver.Type] {
		if !present(field) {
			return fmt.Errorf("config.%s is required", field)
		}
	}
	for _, field := range []string{"retries", "count", "db", "warnDays", "criticalDays"} {
		if raw := fields[field]; len(raw) != 0 && string(raw) != "null" {
			var n int64
			if json.Unmarshal(raw, &n) != nil || n < 0 || n > math.MaxInt32 {
				return fmt.Errorf("config.%s must be between zero and 2147483647", field)
			}
		}
	}
	if category == "check" && (driver.Type == "tcp" || driver.Type == "udp" || driver.Type == "grpc" || driver.Type == "tls") && !referenced("port") {
		var port int
		if json.Unmarshal(fields["port"], &port) != nil || port < 1 || port > 65535 {
			return errors.New("config.port must be between 1 and 65535")
		}
	}
	for _, field := range driverURLFields(category, driver.Type) {
		if !present(field) || referenced(field) {
			continue
		}
		var text string
		u := new(url.URL)
		var err error
		if json.Unmarshal(fields[field], &text) == nil {
			u, err = url.Parse(text)
		}
		if err != nil || u.Hostname() == "" || u.Scheme != "https" && u.Scheme != "http" || strings.ContainsAny(text, "\x00\r\n") {
			return fmt.Errorf("config.%s must be an HTTP or HTTPS URL", field)
		}
	}
	if category == "check" && driver.Type == "grpc" {
		var config PulseGRPCConfig
		_ = json.Unmarshal(driver.Config, &config) // Shape was validated above.
		mode := "tcp"
		if config.Mode != nil {
			mode = string(*config.Mode)
		}
		if mode != "tcp" && mode != "health" {
			return errors.New("config.mode must be tcp or health")
		}
		cleartext := config.AllowInsecure != nil && *config.AllowInsecure
		if mode != "health" && (cleartext || config.CAFile != nil && *config.CAFile != "" || config.ServerName != nil && *config.ServerName != "") {
			return errors.New("gRPC TLS settings require health mode")
		}
		if cleartext && (config.CAFile != nil && *config.CAFile != "" || config.ServerName != nil && *config.ServerName != "") {
			return errors.New("gRPC cleartext cannot also configure TLS trust or server name")
		}
	}
	if category == "recovery" && driver.Type == "docker" {
		grace, err := driverDuration(fields, "timeout")
		if err != nil || grace < 0 || grace > 24*time.Hour {
			return errors.New("Docker stop grace must be between zero and 24h")
		}
		if raw := fields["operationTimeout"]; len(raw) != 0 && string(raw) != "null" {
			budget, err := driverDuration(fields, "operationTimeout")
			if err != nil || budget < grace+20*time.Second || budget > 24*time.Hour {
				return errors.New("Docker operationTimeout must allow the stop grace plus 20s and be at most 24h")
			}
		}
	}
	if category == "notification" && driver.Type == "victorops" && !present("url") && (!present("restEndpointKey") || !present("routingKey")) {
		return errors.New("VictorOps requires url or both restEndpointKey and routingKey")
	}
	return nil
}

var requiredDriverFields = map[string][]string{
	"check/http": {"url"}, "check/tcp": {"host", "port"}, "check/icmp": {"host"},
	"check/dns": {"host"}, "check/udp": {"host", "port"}, "check/grpc": {"host", "port"},
	"check/docker": {"container"}, "check/mongo": {"uri"}, "check/rabbitmq": {"url"},
	"check/kafka": {"brokers"}, "check/tls": {"host", "port"},
	"recovery/docker": {"container"}, "recovery/kubernetes": {"namespace", "kind", "name"},
	"recovery/webhook": {"url"}, "recovery/systemd": {"unit"}, "recovery/aws": {"operation", "instanceId"},
	"recovery/a2a":     {"url"},
	"notification/log": {"file"}, "notification/email": {"from", "to", "server"},
	"notification/webhook": {"url"}, "notification/pagerduty": {"routingKey"},
	"notification/slack": {"hook"}, "notification/discord": {"webhookUrl"},
	"notification/telegram": {"botToken", "chatId"}, "notification/opsgenie": {"apiKey"},
	"notification/teams": {"webhookUrl"}, "notification/mattermost": {"webhookUrl"},
	"notification/pushover": {"appToken", "userKey"}, "notification/twilio": {"accountSid", "authToken", "from", "to"},
	"notification/datadog": {"apiKey"}, "notification/a2a": {"url"},
}

func driverURLFields(category, kind string) []string {
	switch category + "/" + kind {
	case "check/http", "recovery/webhook", "recovery/a2a", "notification/webhook", "notification/pagerduty", "notification/opsgenie", "notification/telegram", "notification/pushover", "notification/twilio", "notification/datadog", "notification/victorops", "notification/a2a":
		return []string{"url"}
	case "notification/slack":
		return []string{"hook"}
	case "notification/discord", "notification/teams", "notification/mattermost":
		return []string{"webhookUrl"}
	}
	return nil
}

func driverDuration(fields map[string]json.RawMessage, field string) (time.Duration, error) {
	raw := fields[field]
	if len(raw) == 0 || string(raw) == "null" {
		return 0, nil
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return 0, errors.New("duration must be a Go duration string")
	}
	return time.ParseDuration(text)
}
