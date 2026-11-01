package api

import (
	"encoding/json"
	"fmt"
)

// NormalizeManifestRecovery translates the file spelling into the canonical
// recovery resource. It preserves omitted counters and performs no I/O. Driver
// settings and match semantics are validated with the enclosing check afterwards.
func NormalizeManifestRecovery(raw json.RawMessage) (json.RawMessage, error) {
	var input map[string]json.RawMessage
	if err := StrictDecode(raw, &input); err != nil {
		return nil, err
	}
	if input == nil {
		return nil, InvalidField("", "objectRequired")
	}
	output := map[string]json.RawMessage{}
	for key, value := range input {
		switch key {
		case "action", "target", "rules":
		case "retries":
			output[key] = value
		case "max_attempts":
			output["maxAttempts"] = value
		case "max_failures":
			output["maxFailures"] = value
		case "taxonomy_version":
			output["taxonomyVersion"] = value
		default:
			return nil, InvalidField("", "unknownField")
		}
	}
	if rules, present := input["rules"]; present {
		if input["action"] != nil || input["target"] != nil {
			return nil, InvalidField("", "recoveryChoice")
		}
		var items []json.RawMessage
		if err := StrictDecode(rules, &items); err != nil {
			return nil, PrefixValidationError("rules", err)
		}
		if len(items) == 0 || len(items) > MaxRecoveryRules {
			return nil, InvalidField("rules", "ruleLimit")
		}
		result := make([]map[string]json.RawMessage, len(items))
		for i, item := range items {
			var values map[string]json.RawMessage
			field := fmt.Sprintf("rules[%d]", i)
			if err := StrictDecode(item, &values); err != nil {
				return nil, PrefixValidationError(field, err)
			}
			if values == nil {
				return nil, InvalidField(field, "objectRequired")
			}
			rule := map[string]json.RawMessage{}
			for key, value := range values {
				switch key {
				case "id", "none":
					rule[key] = value
				case "action", "target":
				case "match":
					var match map[string]json.RawMessage
					if err := StrictDecode(value, &match); err != nil {
						return nil, PrefixValidationError(field+".match", err)
					}
					if match == nil {
						return nil, InvalidField(field+".match", "objectRequired")
					}
					normalized := map[string]json.RawMessage{}
					for name, entry := range match {
						switch name {
						case "codes", "phases":
							normalized[name] = entry
						case "http_statuses":
							normalized["httpStatuses"] = entry
						case "grpc_codes":
							normalized["grpcCodes"] = entry
						case "grpc_statuses":
							normalized["grpcStatuses"] = entry
						default:
							return nil, InvalidField(field+".match", "unknownField")
						}
					}
					rule["match"], _ = json.Marshal(normalized)
				default:
					return nil, InvalidField(field, "unknownField")
				}
			}
			if values["action"] != nil || values["target"] != nil {
				driver, err := manifestRecoveryDriver(values)
				if err != nil {
					return nil, PrefixValidationError(field+".driver", err)
				}
				rule["driver"] = driver
			}
			result[i] = rule
		}
		output["rules"], _ = json.Marshal(result)
	} else {
		driver, err := manifestRecoveryDriver(input)
		if err != nil {
			return nil, PrefixValidationError("driver", err)
		}
		output["driver"] = driver
	}
	return json.Marshal(output)
}

func manifestRecoveryDriver(input map[string]json.RawMessage) (json.RawMessage, error) {
	var kind string
	if json.Unmarshal(input["action"], &kind) != nil || kind == "" {
		return nil, InvalidField("type", "required")
	}
	config, err := NormalizeManifestDriver("recovery", kind, input["target"])
	if err != nil {
		return nil, err
	}
	return json.Marshal(DriverConfig{Type: kind, Config: config})
}
