package manifest

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
	"gopkg.in/yaml.v3"
)

// Custom JSON decoders operate on subdocuments. Their byte offsets are not
// offsets into the caller's document. Preserve a safe field path instead.
func jsonFieldError(prefix string, cause error) error {
	field, reason := "", "invalidType"
	if detail, ok := api.ValidationFieldError(cause); ok {
		field, reason = detail.Field, detail.Reason
	} else {
		var wrong *json.UnmarshalTypeError
		if errors.As(cause, &wrong) {
			field = wrong.Field
			// These original TCP/ICMP fields have no JSON tags. Older Go
			// decoders report their Go name rather than the input spelling.
			// Normalize only the diagnostic; changing their tags would also
			// change serialized configuration and stored revision identities.
			if wrong.Struct == "PulseTCPConfig" || wrong.Struct == "PulseICMPConfig" {
				switch strings.ToLower(field) {
				case "host", "port", "retries", "count", "privilege":
					field = strings.ToLower(field)
				}
			}
			field = api.SafeValidationField(field)
		}
	}
	if field != "" && prefix != "" {
		field = prefix + "." + field
	} else {
		field = prefix + field
	}
	return api.InvalidField(field, reason)
}

// Validate raw scalar duration spelling before YAML materialization collapses
// type errors into text. Values are never attached to the returned diagnostic.
func checkYAMLDurations(value *yaml.Node, prefix string, fields ...string) error {
	var values map[string]yaml.Node
	if err := value.Decode(&values); err != nil {
		return err
	}
	for _, name := range fields {
		if node, ok := values[name]; ok {
			resolved := &node
			for depth := 0; resolved != nil && resolved.Kind == yaml.AliasNode && depth < 128; depth++ {
				resolved = resolved.Alias
			}
			if resolved == nil || resolved.Kind != yaml.ScalarNode || resolved.Tag != "!!str" {
				return api.InvalidField(prefix+"."+name, "invalidType")
			}
			if _, err := time.ParseDuration(resolved.Value); err != nil {
				return api.InvalidField(prefix+"."+name, "positiveDuration")
			}
		}
	}
	return nil
}

func notificationDecodeError(prefix, selector string, cause error) error {
	if detail, ok := api.ValidationFieldError(cause); ok && detail.Reason == "unsupportedDriver" {
		base := strings.TrimSuffix(prefix, ".config")
		if base == "config" {
			base = ""
		}
		if base != "" {
			base += "."
		}
		return api.InvalidField(base+selector, detail.Reason)
	}
	return jsonFieldError(prefix, cause)
}
