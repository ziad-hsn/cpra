package api

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// UnmarshalJSON preserves the difference between an omitted restriction and an
// explicit null. A null/empty restriction must never broaden an intervention.
func (m *FailureMatch) UnmarshalJSON(data []byte) error {
	type plain FailureMatch
	var fields map[string]json.RawMessage
	if err := StrictDecode(data, &fields); err != nil {
		return err
	}
	if fields == nil {
		return InvalidField("match", "objectRequired")
	}
	for name := range fields {
		switch name {
		case "codes", "phases", "httpStatuses", "grpcCodes", "grpcStatuses":
		default:
			return InvalidField("", "unknownField")
		}
	}
	for _, name := range []string{"codes", "phases", "httpStatuses", "grpcCodes", "grpcStatuses"} {
		if raw, exists := fields[name]; exists && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return InvalidField(name, "failureMatcher")
		}
	}
	var value plain
	if err := StrictDecode(data, &value); err != nil {
		return err
	}
	*m = FailureMatch(value)
	return nil
}

func (r *RecoverySpec) UnmarshalJSON(data []byte) error {
	type plain RecoverySpec
	var fields map[string]json.RawMessage
	if err := StrictDecode(data, &fields); err != nil {
		return err
	}
	if fields == nil {
		return InvalidField("", "objectRequired")
	}
	for name := range fields {
		switch name {
		case "driver", "rules", "taxonomyVersion", "retries", "maxFailures", "maxAttempts", "cooldown":
		default:
			return InvalidField("", "unknownField")
		}
	}
	if rules, present := fields["rules"]; present {
		if _, present := fields["driver"]; present {
			return InvalidField("", "recoveryChoice")
		}
		if bytes.Equal(bytes.TrimSpace(rules), []byte("null")) {
			return InvalidField("rules", "ruleLimit")
		}
		var entries []json.RawMessage
		if err := StrictDecode(rules, &entries); err != nil {
			return PrefixValidationError("rules", err)
		}
		if len(entries) > MaxRecoveryRules {
			return InvalidField("rules", "ruleLimit")
		}
		for i, entry := range entries {
			var rule RecoveryRule
			if err := StrictDecode(entry, &rule); err != nil {
				return PrefixValidationError(fmt.Sprintf("rules[%d]", i), err)
			}
		}
	}
	if version, present := fields["taxonomyVersion"]; present && bytes.Equal(bytes.TrimSpace(version), []byte("null")) {
		return InvalidField("taxonomyVersion", "failureTaxonomy")
	}
	var value plain
	if err := StrictDecode(data, &value); err != nil {
		return err
	}
	*r = RecoverySpec(value)
	return nil
}

func (r *RecoveryRule) UnmarshalJSON(data []byte) error {
	type plain RecoveryRule
	var fields map[string]json.RawMessage
	if err := StrictDecode(data, &fields); err != nil {
		return err
	}
	if fields == nil {
		return InvalidField("", "objectRequired")
	}
	for name, raw := range fields {
		switch name {
		case "id", "match", "driver", "none":
		default:
			return InvalidField("", "unknownField")
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return InvalidField(name, "ruleDecision")
		}
	}
	var value plain
	if match, present := fields["match"]; present {
		if err := StrictDecode(match, &value.Match); err != nil {
			return PrefixValidationError("match", err)
		}
	}
	if err := StrictDecode(data, &value); err != nil {
		return err
	}
	*r = RecoveryRule(value)
	return nil
}
