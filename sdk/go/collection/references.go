package collection

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// Reference identifies a dependency independently of its display name.
type Reference struct{ Kind, ID string }

// ResolveReference resolves an omitted, authorized live dependency to its opaque
// resource version. It must return an error for a missing or unauthorized object.
// Versions are observations, not locks: server preflight and activation CAS are
// still mandatory. Nil resolves against the submitted collection only.
type ResolveReference func(context.Context, Reference) (string, error)

// ValidateReferences checks all shared definitions, not only referenced groups.
// It returns observed versions for dependencies omitted from the collection.
// Provider capabilities and permission checks remain server responsibilities.
func ValidateReferences(ctx context.Context, frozen *Frozen, resolve ResolveReference) (map[Reference]string, error) {
	if frozen == nil {
		return nil, fmt.Errorf("frozen collection is required")
	}
	ids := make(map[Reference]bool, frozen.Len())
	if err := frozen.Range(ctx, func(item Item) error {
		ids[Reference{Kind: item.Resource.Kind, ID: item.Resource.Metadata.ID}] = true
		return nil
	}); err != nil {
		return nil, err
	}
	resolved := make(map[Reference]string)
	err := frozen.Range(ctx, func(item Item) error {
		references, err := itemReferences(item)
		if err != nil {
			return fmt.Errorf("%s: %w", item.Location, err)
		}
		for _, reference := range references {
			if ids[reference] {
				continue
			}
			if _, ok := resolved[reference]; ok {
				continue
			}
			if resolve == nil {
				return fmt.Errorf("%s: missing %s/%s", item.Location, reference.Kind, reference.ID)
			}
			version, err := resolve(ctx, reference)
			if err != nil {
				return fmt.Errorf("%s: cannot resolve %s/%s", item.Location, reference.Kind, reference.ID)
			}
			if version == "" {
				return fmt.Errorf("%s: dependency resolver returned no version", item.Location)
			}
			resolved[reference] = version
		}
		return nil
	})
	return resolved, err
}

func itemReferences(item Item) ([]Reference, error) {
	var spec map[string]any
	if err := json.Unmarshal(item.Resource.Spec, &spec); err != nil {
		return nil, fmt.Errorf("invalid resource spec")
	}
	seen := make(map[Reference]bool)
	var result []Reference
	add := func(kind, id string) error {
		if id == "" {
			return fmt.Errorf("empty dependency identity")
		}
		reference := Reference{Kind: kind, ID: id}
		if !seen[reference] {
			seen[reference] = true
			result = append(result, reference)
		}
		return nil
	}
	list := func(kind string, value any, unique bool) error {
		if value == nil {
			return nil
		}
		values, ok := value.([]any)
		if !ok {
			return fmt.Errorf("dependency references must be arrays")
		}
		members := make(map[string]bool)
		for _, value := range values {
			id, ok := value.(string)
			if !ok {
				return fmt.Errorf("dependency identity must be a string")
			}
			if unique && members[id] {
				return fmt.Errorf("duplicate %s reference", kind)
			}
			members[id] = true
			if err := add(kind, id); err != nil {
				return err
			}
		}
		return nil
	}
	if item.Resource.Kind == "NotificationGroup" || item.Resource.Kind == "Recipient" {
		if err := list("NotificationEndpoint", spec["endpointRefs"], true); err != nil {
			return nil, err
		}
	}
	if item.Resource.Kind == "NotificationGroup" {
		if err := list("Recipient", spec["recipientRefs"], true); err != nil {
			return nil, err
		}
	}
	if item.Resource.Kind == "Monitor" {
		if err := list("NotificationGroup", spec["notificationGroupRefs"], false); err != nil {
			return nil, err
		}
		if rules, ok := spec["notifications"].(map[string]any); ok {
			keys := make([]string, 0, len(rules))
			for key := range rules {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				if rule, ok := rules[key].(map[string]any); ok {
					if group, ok := rule["groupRef"].(string); ok {
						if err := add("NotificationGroup", group); err != nil {
							return nil, err
						}
					}
					if err := list("NotificationEndpoint", rule["endpointRefs"], false); err != nil {
						return nil, err
					}
					if err := list("Recipient", rule["recipientRefs"], true); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	// Driver credential refs may appear inside checks, recoveries, and inline
	// notifications. Only structural credentialRefs are interpreted, never maps
	// within driver config or arbitrary metadata/labels.
	var visit func(any) error
	visit = func(value any) error {
		switch value := value.(type) {
		case map[string]any:
			keys := make([]string, 0, len(value))
			for key := range value {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				if key == "config" {
					continue
				}
				if key == "credentialRefs" {
					refs, ok := value[key].(map[string]any)
					if !ok {
						return fmt.Errorf("credentialRefs must be a mapping")
					}
					names := make([]string, 0, len(refs))
					for name := range refs {
						names = append(names, name)
					}
					sort.Strings(names)
					for _, name := range names {
						id, ok := refs[name].(string)
						if !ok {
							return fmt.Errorf("credential identity must be a string")
						}
						if err := add("Credential", id); err != nil {
							return err
						}
					}
				} else if err := visit(value[key]); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range value {
				if err := visit(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := visit(spec); err != nil {
		return nil, err
	}
	if err := additionalReferences(item, add); err != nil {
		return nil, err
	}
	return result, nil
}
