package httpauth

import "github.com/ziad-hsn/cpra/internal/persistence"

// FromAuthentication installs only the committed verifier authority. Transport
// remains deployment configuration; it can never supply an extra credential.
// Initialized policies without active credentials deny all requests.
func FromAuthentication(state persistence.AuthenticationState, proxy *ProxyConfig) (*Authorizer, error) {
	if !persistence.AuthenticationPolicyFormat(state.Version) || !state.BootstrapConsumed || state.ResetRequired || state.AnonymousLoopback {
		return nil, ErrInvalidConfig
	}
	config := Config{LegacyTokenSHA256: state.LegacyTokenSHA256, TrustedProxy: proxy, allowEmpty: true}
	for _, entry := range state.Principals {
		config.Principals = append(config.Principals, Principal{ID: entry.ID, Role: entry.Role, TokenSHA256: entry.TokenSHA256, Revoked: entry.Revoked, ExpiresAt: entry.ExpiresAt})
	}
	return New(config)
}
