package persistence

import "time"

// AuthenticationLifecycleFormatVersion makes named principal IDs permanent
// within one authentication epoch. Version 1 remains a supported historical
// policy; it cannot authorize new background collection work.
const AuthenticationLifecycleFormatVersion = 2

// AuthenticationPolicyFormat recognizes readable policy formats. It does not
// imply eligibility to authorize a background operation.
func AuthenticationPolicyFormat(version int) bool {
	return version == AuthenticationFormatVersion || version == AuthenticationLifecycleFormatVersion
}

// validateAuthenticationLifecycle is part of the same state-machine mutation
// as policy replacement. Historical version-zero commands keep their original
// replay semantics until an explicit current policy establishes this boundary.
func validateAuthenticationLifecycle(old *AuthenticationState, c AuthenticationCommand) error {
	if old != nil && old.Version == AuthenticationLifecycleFormatVersion && c.Version != AuthenticationLifecycleFormatVersion {
		return ErrAuthenticationConflict
	}
	if c.Version != AuthenticationLifecycleFormatVersion || old == nil || c.Mode != "replace" {
		return nil
	}
	next := make(map[string]AuthenticationPrincipal, len(c.Principals))
	for _, principal := range c.Principals {
		next[principal.ID] = principal
	}
	for _, previous := range old.Principals {
		principal, exists := next[previous.ID]
		if !exists {
			return ErrAuthenticationConflict
		}
		if previous.Revoked && (!principal.Revoked || !authenticationPrincipalEqual(previous, principal)) {
			return ErrAuthenticationConflict
		}
	}
	return nil
}

func authenticationPrincipalEqual(a, b AuthenticationPrincipal) bool {
	left, right := a, b
	left.ExpiresAt, right.ExpiresAt = time.Time{}, time.Time{}
	return left == right && a.ExpiresAt.Equal(b.ExpiresAt)
}
