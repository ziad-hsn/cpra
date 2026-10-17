package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"time"
)

// APIVersion is the desired-resource wire version, independent of SDK versions.
const APIVersion = "cpra.io/v2"

// MaxResourceBytes bounds each encoded desired resource to 1 MiB.
const MaxResourceBytes = 1 << 20

// ErrUnsupportedDriver reports a driver absent from this SDK's selected schema.
var ErrUnsupportedDriver = errors.New("driver is unsupported by this SDK build")

// StrictDecode rejects duplicate keys, unknown typed fields and trailing values.
func StrictDecode(data []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := uniqueValue(d, 0); err != nil {
		return jsonValidationError(err)
	}
	if _, err := d.Token(); err != io.EOF {
		return InvalidField("", "invalidJSON")
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return jsonValidationError(d.Decode(out))
}
func uniqueValue(d *json.Decoder, depth int) error {
	if depth > 128 {
		return InvalidField("", "nodeLimit")
	}
	t, err := d.Token()
	if err != nil {
		return err
	}
	switch t {
	case json.Delim('{'):
		seen := map[string]bool{}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := k.(string)
			if !ok {
				return errors.New("invalid object key")
			}
			if seen[key] {
				return InvalidField("", "duplicateField")
			}
			seen[key] = true
			if err := uniqueValue(d, depth+1); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	case json.Delim('['):
		for d.More() {
			if err := uniqueValue(d, depth+1); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	}
	return nil
}

// DecodeResource validates a source-neutral resource envelope.
func DecodeResource(raw []byte) (Resource, error) {
	var r Resource
	if len(raw) > MaxResourceBytes {
		return r, InvalidField("", "resourceTooLarge")
	}
	if err := StrictDecode(raw, &r); err != nil {
		return r, err
	}
	return r, ValidateResource(r)
}

// ValidateResourceValue validates a typed resource before mutation.
func ValidateResourceValue(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	r, err := DecodeResource(raw)
	if err != nil {
		return err
	}
	return ValidateResourceSettings(r)
}

// ValidateResource checks a resource envelope, spec, and selected driver shapes.
// It performs no server authorization, live dependency check, or provider I/O.
func ValidateResource(r Resource) error {
	if r.APIVersion != APIVersion {
		return InvalidField("apiVersion", "unsupportedVersion")
	}
	if r.Metadata.ID == "" || r.Metadata.ID == "." || r.Metadata.ID == ".." || strings.ContainsAny(r.Metadata.ID, "/\\?#%\r\n") {
		return InvalidField("metadata.id", "invalidIdentity")
	}
	switch r.Kind {
	case "Monitor":
		var spec MonitorSpec
		if err := StrictDecode(r.Spec, &spec); err != nil {
			return PrefixValidationError("spec", err)
		}
		if err := ValidateDriver("check", spec.Check.Driver); err != nil {
			return PrefixValidationError("spec.check.driver", err)
		}
		for _, field := range []struct{ name, value string }{{"interval", spec.Check.Interval}, {"timeout", spec.Check.Timeout}} {
			v, e := time.ParseDuration(field.value)
			if e != nil || v <= 0 {
				return InvalidField("spec.check."+field.name, "positiveDuration")
			}
		}
		if spec.Recovery != nil {
			if err := ValidateDriver("recovery", spec.Recovery.Driver); err != nil {
				return PrefixValidationError("spec.recovery.driver", err)
			}
		}
		if spec.Notifications != nil {
			for color, rule := range *spec.Notifications {
				if rule.Trigger != nil {
					switch string(*rule.Trigger) {
					case "state_change":
					case "recovery_failed", "recovery_exhausted":
						if color != "red" || spec.Recovery == nil {
							return InvalidField("spec.notifications", "notificationTrigger")
						}
					default:
						return InvalidField("spec.notifications", "notificationTrigger")
					}
				}
				if err := validateAlertRule(rule); err != nil {
					return PrefixValidationError(notificationField(color), err)
				}
				if rule.Driver != nil {
					if err := ValidateDriver("notification", *rule.Driver); err != nil {
						return PrefixValidationError(notificationField(color)+".driver", err)
					}
				}
			}
		}
	case "NotificationEndpoint":
		var spec DriverConfig
		if err := StrictDecode(r.Spec, &spec); err != nil {
			return PrefixValidationError("spec", err)
		}
		return PrefixValidationError("spec", ValidateDriver("notification", spec))
	case "NotificationGroup":
		var spec NotificationGroupSpec
		if err := StrictDecode(r.Spec, &spec); err != nil {
			return PrefixValidationError("spec", err)
		}
		if len(spec.EndpointRefs)+len(spec.RecipientRefs) == 0 {
			return InvalidField("spec", "nonemptyReferences")
		}
		if err := validateReferences("endpointRefs", spec.EndpointRefs); err != nil {
			return PrefixValidationError("spec", err)
		}
		return PrefixValidationError("spec", validateReferences("recipientRefs", spec.RecipientRefs))
	case "Recipient":
		var spec RecipientSpec
		if err := StrictDecode(r.Spec, &spec); err != nil {
			return PrefixValidationError("spec", err)
		}
		if len(spec.EndpointRefs) == 0 {
			return InvalidField("spec.endpointRefs", "nonemptyReferences")
		}
		return PrefixValidationError("spec", validateReferences("endpointRefs", spec.EndpointRefs))
	case "Credential":
		var spec CredentialSpec
		if err := StrictDecode(r.Spec, &spec); err != nil {
			return PrefixValidationError("spec", err)
		}
	default:
		return validateAdditionalResource(r)
	}
	return nil
}

func validateReferences(field string, refs []string) error {
	seen := make(map[string]bool, len(refs))
	for _, id := range refs {
		if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\?#%\r\n") || seen[id] {
			return InvalidField(field, "invalidReference")
		}
		seen[id] = true
	}
	return nil
}

// validateAlertRule checks structural routing only. Matching contact methods and
// reverse dependencies require the server's authoritative complete catalog.
func validateAlertRule(rule AlertRule) error {
	if rule.RecipientRefs != nil {
		if err := validateReferences("recipientRefs", *rule.RecipientRefs); err != nil {
			return err
		}
		if rule.NotifyType == nil {
			return InvalidField("notifyType", "notifyTypeRequired")
		}
	}
	if rule.NotifyType == nil {
		return nil // Direct endpoint and group routing permits mixed driver types.
	}
	if driverTarget("notification", *rule.NotifyType) == nil {
		return errors.Join(ErrUnsupportedDriver, InvalidField("notifyType", "unsupportedDriver"))
	}
	if rule.Driver != nil || rule.EndpointRefs != nil {
		return InvalidField("notifyType", "notificationDestination")
	}
	if rule.GroupRef != nil {
		if err := validateReferences("groupRef", []string{*rule.GroupRef}); err != nil {
			return err
		}
	}
	if rule.GroupRef == nil && (rule.RecipientRefs == nil || len(*rule.RecipientRefs) == 0) {
		return InvalidField("notifyType", "nonemptyReferences")
	}
	return nil
}

// Driver constructs a concrete driver envelope while preserving optional fields.
func Driver(category, kind string, config any) (DriverConfig, error) {
	raw, err := json.Marshal(config)
	d := DriverConfig{Type: kind, Config: raw}
	if err != nil {
		return d, err
	}
	return d, ValidateDriver(category, d)
}

// ValidateDriver rejects unknown variants on mutation; ordinary read decoding
// retains their type and raw configuration for inspection.
func ValidateDriver(category string, d DriverConfig) error {
	target := driverTarget(category, d.Type)
	if target == nil {
		return errors.Join(ErrUnsupportedDriver, InvalidField("type", "unsupportedDriver"))
	}
	if len(d.Config) == 0 || bytes.Equal(bytes.TrimSpace(d.Config), []byte("null")) {
		return InvalidField("config", "objectRequired")
	}
	return PrefixValidationError("config", StrictDecode(d.Config, target))
}

// NormalizeManifestDriver maps manifest driver fields to their API names.
// User-defined map keys, such as HTTP headers, retain their original spelling.
func NormalizeManifestDriver(category, kind string, raw json.RawMessage) (json.RawMessage, error) {
	target := driverTarget(category, kind)
	if target == nil {
		return nil, ErrUnsupportedDriver
	}
	var fields map[string]json.RawMessage
	if err := StrictDecode(raw, &fields); err != nil {
		return nil, PrefixValidationError("config", err)
	}
	t := reflect.TypeOf(target).Elem()
	names := map[string]string{}
	for i := 0; i < t.NumField(); i++ {
		k := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		names[strings.ToLower(strings.ReplaceAll(k, "_", ""))] = k
	}
	if category == "check" && kind == "icmp" {
		// Existing JSON manifests use the Go field name; YAML uses the tag.
		names["privilege"] = "ignorePrivilege"
	}
	result := map[string]json.RawMessage{}
	for k, v := range fields {
		n, ok := names[strings.ToLower(strings.ReplaceAll(k, "_", ""))]
		if !ok {
			return nil, InvalidField("config", "unknownField")
		}
		if _, exists := result[n]; exists {
			return nil, InvalidField("config", "duplicateField")
		}
		if (n == "timeout" || n == "operationTimeout" || n == "requestTimeout" || n == "taskTimeout" || n == "pollInterval") && len(v) > 0 && v[0] != '"' && string(v) != "null" {
			var nanos int64
			if err := json.Unmarshal(v, &nanos); err != nil {
				return nil, PrefixValidationError("config."+n, jsonValidationError(err))
			}
			v, _ = json.Marshal(time.Duration(nanos).String())
		}
		result[n] = v
	}
	out, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return out, ValidateDriver(category, DriverConfig{Type: kind, Config: out})
}

// Pointer returns a pointer to a copy of value, preserving optional false and
// zero values without losing presence.
func Pointer[T any](value T) *T { return &value }

// Do not include arbitrary notification-map keys in a diagnostic path.
func notificationField(color string) string {
	switch color {
	case "red", "yellow", "green", "cyan", "gray":
		return "spec.notifications." + color
	}
	return "spec.notifications"
}
