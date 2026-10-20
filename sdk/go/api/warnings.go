package api

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
	if spec.Notifications != nil {
		if blue, ok := (*spec.Notifications)["blue"]; ok && (blue.Dispatch == nil || *blue.Dispatch) {
			if blue.Driver != nil || blue.GroupRef != nil || blue.EndpointRefs != nil && len(*blue.EndpointRefs) > 0 || blue.RecipientRefs != nil && len(*blue.RecipientRefs) > 0 {
				return nil
			}
		}
	}
	return []FieldError{{Field: "spec.notifications.blue", Reason: "unknownRecoveryDestination", Message: ValidationMessage("unknownRecoveryDestination")}}
}
