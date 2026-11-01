package api

import "fmt"

// ResourceWarnings returns bounded, value-free advisories about a valid desired
// resource. It performs no provider, credential or filesystem access. Warnings
// never replace validation or make an otherwise valid resource invalid.
func ResourceWarnings(resource Resource) []FieldError {
	if resource.Kind != "Monitor" {
		return nil
	}
	var spec MonitorSpec
	if StrictDecode(resource.Spec, &spec) != nil || spec.Recovery == nil {
		return nil
	}
	var warnings []FieldError
	if len(spec.Recovery.Rules) > 0 && spec.Check.Driver.Type != "external" {
		if _, overlaps, err := CompileRecoveryRules(spec.Check.Driver, *spec.Recovery); err == nil {
			warnings = append(warnings, RecoveryRuleWarnings(*spec.Recovery, overlaps)...)
		}
	}
	hasAction := spec.Recovery.Driver.Type != ""
	for _, rule := range spec.Recovery.Rules {
		hasAction = hasAction || rule.Driver != nil
	}
	if spec.Notifications != nil {
		if red, ok := (*spec.Notifications)["red"]; ok && (red.Dispatch == nil || *red.Dispatch) && red.Trigger != nil && (*red.Trigger == "recovery_failed" || *red.Trigger == "recovery_exhausted") && (len(spec.Recovery.Rules) > 0 || !hasAction) {
			warnings = append(warnings, FieldError{Field: "spec.notifications.red.trigger", Reason: "recoveryOnlyCoverage", Message: ValidationMessage("recoveryOnlyCoverage")})
		}
	}
	if !hasAction {
		return warnings
	}
	if spec.Notifications != nil {
		if blue, ok := (*spec.Notifications)["blue"]; ok && (blue.Dispatch == nil || *blue.Dispatch) {
			if blue.Driver != nil || blue.GroupRef != nil || blue.EndpointRefs != nil && len(*blue.EndpointRefs) > 0 || blue.RecipientRefs != nil && len(*blue.RecipientRefs) > 0 {
				return warnings
			}
		}
	}
	return append(warnings, FieldError{Field: "spec.notifications.blue", Reason: "unknownRecoveryDestination", Message: ValidationMessage("unknownRecoveryDestination")})
}

// RecoveryRuleWarnings maps a compiled policy's bounded advisories to the
// original rule fields. It includes no action settings or remote messages.
func RecoveryRuleWarnings(recovery RecoverySpec, warnings []RuleWarning) []FieldError {
	var out []FieldError
	for i, rule := range recovery.Rules {
		for _, warning := range warnings {
			if rule.ID == warning.RuleID && warning.Reason == "partialOverlap" {
				out = append(out, FieldError{Field: fmt.Sprintf("spec.recovery.rules[%d]", i), Reason: "partialOverlap", Message: ValidationMessage("partialOverlap")})
				break
			}
		}
	}
	return out
}
