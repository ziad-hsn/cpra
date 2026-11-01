package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/mail"
	"net/url"
	"slices"
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
		if err := ValidateMonitorSettings(spec); err != nil {
			return PrefixValidationError("spec", err)
		}
		if err := ValidateAutoIgnore(spec.AutoIgnore); err != nil {
			return PrefixValidationError("spec", err)
		}
		if err := ValidateDriverSettings("check", spec.Check.Driver); err != nil {
			return PrefixValidationError("spec.check.driver", err)
		}
		if err := ValidateRecoverySettings(spec.Check.Driver, spec.Recovery); err != nil {
			return PrefixValidationError("spec.recovery", err)
		}
		if spec.Notifications != nil {
			for color, rule := range *spec.Notifications {
				if rule.Driver != nil {
					if err := ValidateDriverSettings("notification", *rule.Driver); err != nil {
						return PrefixValidationError("spec.notifications."+color+".driver", err)
					}
				}
			}
		}
	case "NotificationEndpoint":
		var driver DriverConfig
		_ = json.Unmarshal(resource.Spec, &driver)
		return PrefixValidationError("spec", ValidateDriverSettings("notification", driver))
	}
	return nil
}

// ValidateAutoIgnore validates explicit incident count/window without I/O.
func ValidateAutoIgnore(policy *AutoIgnore) error {
	if policy == nil {
		return nil
	}
	if policy.Incidents < 2 || policy.Incidents > 1000 {
		return InvalidField("autoIgnore.incidents", "autoIgnoreCount")
	}
	window, err := time.ParseDuration(policy.Window)
	if err != nil || window <= 0 || window > 720*time.Hour {
		return InvalidField("autoIgnore.window", "autoIgnoreWindow")
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
	if category == "check" {
		if err := ValidateTLSSettings(driver); err != nil {
			return err
		}
	}
	var fields map[string]json.RawMessage
	if err := StrictDecode(driver.Config, &fields); err != nil || fields == nil {
		return InvalidField("config", "objectRequired")
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
			return InvalidField("config."+field, "required")
		}
	}
	for _, field := range []string{"retries", "count", "db", "warnDays", "criticalDays", "replicas"} {
		if raw := fields[field]; len(raw) != 0 && string(raw) != "null" {
			var n int64
			if json.Unmarshal(raw, &n) != nil || n < 0 || n > math.MaxInt32 {
				return InvalidField("config."+field, "counterRange")
			}
		}
	}
	if category == "check" && (driver.Type == "tcp" || driver.Type == "udp" || driver.Type == "grpc" || driver.Type == "tls") && !referenced("port") {
		var port int
		if json.Unmarshal(fields["port"], &port) != nil || port < 1 || port > 65535 {
			return InvalidField("config.port", "portRange")
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
		if err != nil || u.Hostname() == "" || u.Fragment != "" || !validURLPort(u) || u.Scheme != "https" && u.Scheme != "http" || strings.ContainsAny(text, "\x00\r\n") {
			return InvalidField("config."+field, "httpURL")
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
			return InvalidField("config.mode", "grpcMode")
		}
		cleartext := config.AllowInsecure != nil && *config.AllowInsecure
		if mode != "health" && (cleartext || config.CAFile != nil && *config.CAFile != "" || config.ServerName != nil && *config.ServerName != "" || config.Service != nil && *config.Service != "" || referenced("service") || referenced("caFile") || referenced("serverName")) {
			return InvalidField("config.mode", "grpcHealthSettings")
		}
		if cleartext && (config.CAFile != nil && *config.CAFile != "" || config.ServerName != nil && *config.ServerName != "") {
			return InvalidField("config.allowInsecure", "cleartextTLSConflict")
		}
	}
	if category == "recovery" && driver.Type == "docker" {
		grace, err := driverDuration(fields, "timeout")
		if err != nil || grace < 0 || grace > 24*time.Hour-20*time.Second {
			return InvalidField("config.timeout", "dockerStopBudget")
		}
		if raw := fields["operationTimeout"]; len(raw) != 0 && string(raw) != "null" {
			budget, err := driverDuration(fields, "operationTimeout")
			if err != nil || budget < grace+20*time.Second || budget > 24*time.Hour {
				return InvalidField("config.operationTimeout", "dockerOperationBudget")
			}
		}
	}
	if category == "recovery" && driver.Type != "docker" && driver.Type != "a2a" && present("timeout") && !referenced("timeout") {
		budget, err := driverDuration(fields, "timeout")
		if err != nil || budget < 0 || budget > 24*time.Hour {
			return InvalidField("config.timeout", "recoveryTimeout")
		}
	}
	if category == "notification" && driver.Type == "victorops" && !present("url") && (!present("restEndpointKey") || !present("routingKey")) {
		return InvalidField("config", "victoropsDestination")
	}
	if category == "notification" && driver.Type == "log" && !referenced("file") {
		var file string
		_ = json.Unmarshal(fields["file"], &file)
		if err := ValidateNotificationLogPath(file); err != nil {
			return err
		}
	}
	text := func(name string) string {
		var value string
		if !referenced(name) {
			_ = json.Unmarshal(fields[name], &value)
		}
		return value
	}
	if category == "check" && driver.Type == "tls" && !referenced("warnDays") && !referenced("criticalDays") {
		var warning, critical int64
		_ = json.Unmarshal(fields["warnDays"], &warning)
		_ = json.Unmarshal(fields["criticalDays"], &critical)
		if warning > 0 && critical > 0 && warning < critical {
			return InvalidField("config.warnDays", "certificateThresholds")
		}
	}
	if raw := fields["headers"]; len(raw) != 0 && !referenced("headers") {
		var headers map[string]string
		_ = json.Unmarshal(raw, &headers)
		for name, value := range headers {
			if name == "" || strings.ContainsFunc(value, func(r rune) bool { return r < 32 && r != '\t' || r == 127 }) {
				return InvalidField("config.headers", "httpHeaders")
			}
			for _, r := range name {
				if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", r)) {
					return InvalidField("config.headers", "httpHeaders")
				}
			}
		}
	}
	if category == "check" && driver.Type == "mysql" && !referenced("dsn") {
		if err := validateMySQLFilePolicy(text("dsn")); err != nil {
			return err
		}
	}
	if method := text("method"); method != "" {
		if _, err := http.NewRequest(method, "https://example.invalid", nil); err != nil {
			return InvalidField("config.method", "httpMethod")
		}
	}
	if raw := fields["expectedStatus"]; len(raw) != 0 && !referenced("expectedStatus") {
		var statuses []int
		_ = json.Unmarshal(raw, &statuses)
		for _, status := range statuses {
			if status < 100 || status > 599 {
				return InvalidField("config.expectedStatus", "httpStatus")
			}
		}
	}
	if category == "check" && driver.Type == "dns" && text("server") != "" && !validServerAddress(text("server"), true) {
		return InvalidField("config.server", "dnsServer")
	}
	if category == "recovery" {
		switch driver.Type {
		case "kubernetes":
			if !referenced("kind") && text("kind") != "deployment" {
				return InvalidField("config.kind", "kubernetesKind")
			}
		case "aws":
			if !referenced("operation") && text("operation") != "reboot-instance" {
				return InvalidField("config.operation", "awsOperation")
			}
		case "systemd":
			if mode := text("mode"); mode != "" && !slices.Contains([]string{"replace", "fail", "isolate", "flush", "ignore-dependencies", "ignore-requirements", "replace-irreversibly"}, mode) {
				return InvalidField("config.mode", "systemdMode")
			}
		}
	}
	if category == "notification" {
		switch driver.Type {
		case "email":
			if !referenced("from") {
				if _, err := mail.ParseAddress(text("from")); err != nil {
					return InvalidField("config.from", "emailAddress")
				}
			}
			if !referenced("to") {
				if addresses, err := mail.ParseAddressList(text("to")); err != nil || len(addresses) == 0 {
					return InvalidField("config.to", "emailAddresses")
				}
			}
			if !referenced("server") && !validServerAddress(text("server"), false) {
				return InvalidField("config.server", "hostPort")
			}
			if strings.ContainsAny(text("subject"), "\r\n") {
				return InvalidField("config.subject", "noLineBreaks")
			}
		case "telegram":
			var testMode bool
			_ = json.Unmarshal(fields["testMode"], &testMode)
			if testMode && present("url") {
				return InvalidField("config.testMode", "telegramDestinationConflict")
			}
		case "pushover":
			var priority, retry, expire int
			_ = json.Unmarshal(fields["priority"], &priority)
			_ = json.Unmarshal(fields["retry"], &retry)
			_ = json.Unmarshal(fields["expire"], &expire)
			emergency := priority == 2
			var overrides map[string]int
			_ = json.Unmarshal(fields["priorityByColor"], &overrides)
			for color, value := range overrides {
				if !slices.Contains([]string{"yellow", "red", "cyan", "green", "gray", "blue"}, color) || value < -2 || value > 2 {
					return InvalidField("config.priorityByColor", "pushoverColorPriority")
				}
				emergency = emergency || value == 2
			}
			if priority < -2 || priority > 2 || retry < 0 || expire < 0 || emergency && (retry != 0 && retry < 30 || expire > 10800) {
				return InvalidField("config", "pushoverEmergency")
			}
		}
	}
	if category == "recovery" && !referenced("type") && text("type") != "" && text("type") != driver.Type {
		return InvalidField("config.type", "driverTypeConflict")
	}
	return validateDriverTargets(category, driver, fields)
}

func validateMySQLFilePolicy(dsn string) error {
	// Match the pinned driver's parameter boundary without importing an execution
	// library into the SDK. Runtime admission still checks mysql.ParseDSN itself.
	_, query, found := strings.Cut(dsn[strings.LastIndex(dsn, "/")+1:], "?")
	if !found {
		return nil
	}
	var selected string
	for _, parameter := range strings.Split(query, "&") {
		key, value, ok := strings.Cut(parameter, "=")
		if ok && key == "allowAllFiles" {
			selected = value
		}
	}
	switch selected {
	case "", "0", "false", "FALSE", "False":
		return nil
	default:
		return InvalidField("config.dsn", "mysqlFileAccess")
	}
}

var requiredDriverFields = map[string][]string{
	"check/http": {"url"}, "check/tcp": {"host", "port"}, "check/icmp": {"host"},
	"check/dns": {"host"}, "check/udp": {"host", "port", "payload"}, "check/grpc": {"host", "port"},
	"check/docker": {"container"}, "check/mongo": {"uri"}, "check/rabbitmq": {"url"},
	"check/kafka": {"brokers"}, "check/tls": {"host", "port"},
	"recovery/docker": {"container"}, "recovery/kubernetes": {"namespace", "kind", "name"},
	"recovery/webhook": {"url"}, "recovery/systemd": {"unit"}, "recovery/aws": {"operation", "instanceId"},
	"recovery/a2a":         {"url"},
	"notification/email":   {"from", "to", "server"},
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
