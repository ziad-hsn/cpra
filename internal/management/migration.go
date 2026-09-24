package management

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// ExtractInlineCredentials converts an inert bootstrap resource into reference
// form. It performs no storage or activation. The returned credentials contain
// plaintext and must go directly into encrypted staging. Ordinary API writes
// retain their explicit-reference requirement; they do not call this converter.
//
// Generated identities depend on owner kind/ID and field purpose, never secret
// values. Restarting the same staged migration cannot invent another credential
// identity, and rotating a value preserves that identity. Duplicate identities
// against user-supplied resources must be rejected by staging, not overwritten.
func ExtractInlineCredentials(input api.Resource) (api.Resource, []api.Resource, error) {
	raw, err := json.Marshal(input)
	if err != nil || len(raw) > api.MaxResourceBytes {
		return api.Resource{}, nil, ErrValidation
	}
	resource, err := api.DecodeResource(raw)
	if err != nil || !supportedKind(resource.Kind) || !validID(resource.Metadata.ID) {
		return api.Resource{}, nil, ErrValidation
	}
	var credentials []api.Resource
	extract := func(path, category string, driver *api.DriverConfig) error {
		if api.ValidateDriver(category, *driver) != nil {
			return ErrValidation
		}
		var fields map[string]json.RawMessage
		if api.StrictDecode(driver.Config, &fields) != nil || fields == nil {
			return ErrValidation
		}
		protected := ProtectedDriverFields(category, driver.Type)
		if category == "check" && driver.Type == "http" {
			if raw := fields["url"]; len(raw) > 0 && string(raw) != "null" {
				var value string
				if json.Unmarshal(raw, &value) != nil {
					return ErrValidation
				}
				parsed, err := url.Parse(value)
				if err != nil {
					return ErrValidation
				}
				if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
					protected = append(protected, "url")
				}
			}
		}
		refs := map[string]string{}
		if driver.CredentialRefs != nil {
			for key, value := range *driver.CredentialRefs {
				refs[key] = value
			}
		}
		for _, field := range protected {
			value, ok := fields[field]
			if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				continue
			}
			if _, conflict := refs[field]; conflict {
				return fmt.Errorf("%w: inline value conflicts with an existing credential reference", ErrValidation)
			}
			var text string
			if json.Unmarshal(value, &text) != nil {
				text = string(value)
			}
			// Optional zero-value fields keep their constructor defaults. Required
			// empty fields still fail subsequent resolved semantic validation.
			delete(fields, field)
			if text == "" {
				continue
			}
			purpose := path + "/" + field
			identityJSON, _ := json.Marshal([]string{"cpra-private-credential-v1", resource.Kind, resource.Metadata.ID, purpose})
			sum := sha256.Sum256(identityJSON)
			id := "private:" + hex.EncodeToString(sum[:])
			spec, _ := json.Marshal(api.CredentialSpec{Value: &text, Description: api.Pointer("Imported " + resource.Kind + " credential for " + purpose)})
			credentials = append(credentials, api.Resource{APIVersion: api.APIVersion, Kind: "Credential", Metadata: api.Metadata{ID: id}, Spec: spec})
			refs[field] = id
		}
		driver.Config, _ = json.Marshal(fields)
		if len(refs) > 0 {
			driver.CredentialRefs = &refs
		} else {
			driver.CredentialRefs = nil
		}
		return validateProtectedDriver(category, *driver)
	}
	switch resource.Kind {
	case "NotificationEndpoint":
		var driver api.DriverConfig
		if api.StrictDecode(resource.Spec, &driver) != nil {
			return api.Resource{}, nil, ErrValidation
		}
		if err := extract("notification", "notification", &driver); err != nil {
			return api.Resource{}, nil, err
		}
		resource.Spec, _ = json.Marshal(driver)
	case "Monitor":
		var spec api.MonitorSpec
		if api.StrictDecode(resource.Spec, &spec) != nil {
			return api.Resource{}, nil, ErrValidation
		}
		if err := extract("check", "check", &spec.Check.Driver); err != nil {
			return api.Resource{}, nil, err
		}
		if spec.Recovery != nil {
			if err := extract("recovery", "recovery", &spec.Recovery.Driver); err != nil {
				return api.Resource{}, nil, err
			}
		}
		if spec.Notifications != nil {
			colors := make([]string, 0, len(*spec.Notifications))
			for color := range *spec.Notifications {
				colors = append(colors, color)
			}
			slices.Sort(colors)
			for _, color := range colors {
				rule := (*spec.Notifications)[color]
				if rule.Driver != nil {
					if err := extract("notification/"+color, "notification", rule.Driver); err != nil {
						return api.Resource{}, nil, err
					}
					(*spec.Notifications)[color] = rule
				}
			}
		}
		resource.Spec, _ = json.Marshal(spec)
	}
	slices.SortFunc(credentials, func(a, b api.Resource) int { return strings.Compare(a.Metadata.ID, b.Metadata.ID) })
	return resource, credentials, nil
}
