package management

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// NotificationCatalog is an immutable candidate view supplied by the catalog
// owner. Callers must not modify its resources concurrently with resolution.
// It contains only the bounded shared graph needed for the requested validation.
type NotificationCatalog struct {
	Endpoints  map[string]api.NotificationEndpoint
	Recipients map[string]api.Recipient
	Groups     map[string]api.NotificationGroup
}

// NotificationReference identifies an observed dependency for conditional
// validation. Credential references and revisions are added by the catalog owner.
type NotificationReference struct {
	Kind            string
	ID              string
	UID             string
	ResourceVersion string
}

// NotificationTarget identifies one endpoint incarnation without copying its
// provider configuration or secrets into an intent or diagnostic result.
type NotificationTarget struct {
	EndpointID      string
	EndpointUID     string
	ResourceVersion string
	DriverType      string
}

// NotificationResolution preserves first-occurrence delivery order. A nil target
// list and nil InlineDriver is valid only for an empty, non-dispatching legacy rule.
// Prospective resources can have empty UIDs during preflight; the owner must assign
// and freeze committed identities before dispatch and revalidate dependencies.
type NotificationResolution struct {
	Targets      []NotificationTarget
	InlineDriver *api.DriverConfig
	Dependencies []NotificationReference
}

// These private lookup contracts are implemented only by an authenticated,
// immutable selected graph. They expose routing facts, never provider settings.
// Generic/map lookups still validate the full resource on every borrow.
type routingEndpointLookup interface {
	withRoutingEndpoint(context.Context, persistence.CatalogKey, func(api.Metadata, string) error) (bool, error)
}

type routingExistenceLookup interface {
	routingExists(context.Context, persistence.CatalogKey) (bool, error)
}

// ResolveNotificationRule resolves a Code without constructing jobs or invoking
// providers. Ordering is direct recipients, group endpoints, then group recipients.
// Filtered endpoints remain dependencies and duplicate endpoint incarnations are
// delivered only once. Callers must keep the supplied catalog immutable.
func ResolveNotificationRule(rule api.AlertRule, catalog NotificationCatalog) (NotificationResolution, error) {
	return resolveNotificationRule(rule, catalog, nil)
}

func resolveNotificationRule(rule api.AlertRule, catalog NotificationCatalog, visit func() error) (NotificationResolution, error) {
	return resolveNotificationRuleLookup(context.Background(), rule, notificationCatalogLookup{catalog}, visit)
}

// A nil sink validates the same expansion without retaining resolved targets,
// dependency records, or either deduplication map. Explicit duplicate references
// are still rejected and repeated expansions still consume the caller's work limit.
type notificationSink struct {
	result       NotificationResolution
	dependencies map[NotificationReference]bool
	targets      map[[2]string]bool
}

func (s *notificationSink) dependency(kind string, meta api.Metadata) {
	if s == nil {
		return
	}
	ref := NotificationReference{Kind: kind, ID: meta.ID, UID: meta.UID, ResourceVersion: meta.ResourceVersion}
	if !s.dependencies[ref] {
		ref.ID, ref.UID, ref.ResourceVersion = strings.Clone(ref.ID), strings.Clone(ref.UID), strings.Clone(ref.ResourceVersion)
		s.dependencies[ref] = true
		s.result.Dependencies = append(s.result.Dependencies, ref)
	}
}

func (s *notificationSink) endpoint(meta api.Metadata, kind string) {
	if s == nil {
		return
	}
	key := [2]string{meta.ID, meta.UID}
	if !s.targets[key] {
		key = [2]string{strings.Clone(meta.ID), strings.Clone(meta.UID)}
		s.targets[key] = true
		s.result.Targets = append(s.result.Targets, NotificationTarget{EndpointID: key[0], EndpointUID: key[1], ResourceVersion: strings.Clone(meta.ResourceVersion), DriverType: strings.Clone(kind)})
	}
}

func resolveNotificationRuleLookup(ctx context.Context, rule api.AlertRule, lookup resourceLookup, visit func() error) (NotificationResolution, error) {
	sink := &notificationSink{dependencies: make(map[NotificationReference]bool), targets: make(map[[2]string]bool)}
	if err := walkNotificationRule(ctx, rule, lookup, visit, nil, sink); err != nil {
		if sink.result.InlineDriver != nil {
			clear(sink.result.InlineDriver.Config)
		}
		return NotificationResolution{}, err
	}
	return sink.result, nil
}

func validateNotificationRuleLookup(ctx context.Context, rule api.AlertRule, lookup resourceLookup, visit func() error, scratch reserveResourceScratch) error {
	return walkNotificationRule(ctx, rule, lookup, visit, scratch, nil)
}

func walkNotificationRule(ctx context.Context, rule api.AlertRule, lookup resourceLookup, visit func() error, scratch reserveResourceScratch, sink *notificationSink) error {
	if ctx == nil || lookup == nil {
		return ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	anyTarget := false
	endpoint := func(id, selected string) (matched bool, err error) {
		err = withRoutingEndpoint(ctx, lookup, persistence.CatalogKey{Kind: "NotificationEndpoint", ID: id}, visit, scratch,
			func(meta api.Metadata, driverType string) error {
				sink.dependency("NotificationEndpoint", meta)
				if selected != "" && driverType != selected {
					return nil
				}
				matched, anyTarget = true, true
				sink.endpoint(meta, driverType)
				return nil
			})
		return matched, err
	}
	recipient := func(id, selected string) error {
		return withRoutingSpec(ctx, lookup, persistence.CatalogKey{Kind: "Recipient", ID: id}, visit, scratch,
			func(meta api.Metadata, spec api.RecipientSpec) error {
				sink.dependency("Recipient", meta)
				matched := false
				for _, endpointID := range spec.EndpointRefs {
					match, err := endpoint(endpointID, selected)
					if err != nil {
						return fmt.Errorf("recipient %q: %w", id, err)
					}
					matched = matched || match
				}
				if !matched {
					return fmt.Errorf("recipient %q has no endpoint matching Code notifyType %q", id, selected)
				}
				return nil
			})
	}
	resolve := func(group *api.NotificationGroupSpec) error {
		selected := ""
		if rule.NotifyType != nil {
			selected = *rule.NotifyType
			if err := api.ValidateDriver("notification", api.DriverConfig{Type: selected, Config: json.RawMessage(`{}`)}); err != nil {
				return fmt.Errorf("invalid Code notifyType: %w", err)
			}
			if rule.Driver != nil || rule.EndpointRefs != nil {
				return fmt.Errorf("typed Code cannot combine notifyType with inline driver or endpointRefs")
			}
			if group == nil && (rule.RecipientRefs == nil || len(*rule.RecipientRefs) == 0) {
				return fmt.Errorf("typed Code requires a recipient or notification group")
			}
		} else if rule.RecipientRefs != nil || (group != nil && len(group.RecipientRefs) > 0) {
			return fmt.Errorf("select Code notification type (notifyType) before targeting recipients")
		}
		if rule.RecipientRefs != nil {
			seen := make(map[string]bool)
			for _, id := range *rule.RecipientRefs {
				if seen[id] {
					return fmt.Errorf("duplicate recipient reference %q", id)
				}
				seen[id] = true
				if err := recipient(id, selected); err != nil {
					return err
				}
			}
		}
		if rule.EndpointRefs != nil {
			for _, id := range *rule.EndpointRefs {
				if _, err := endpoint(id, selected); err != nil {
					return err
				}
			}
		}
		if group != nil {
			for _, id := range group.EndpointRefs {
				if _, err := endpoint(id, selected); err != nil {
					return fmt.Errorf("notification group %q: %w", *rule.GroupRef, err)
				}
			}
			for _, id := range group.RecipientRefs {
				if err := recipient(id, selected); err != nil {
					return fmt.Errorf("notification group %q: %w", *rule.GroupRef, err)
				}
			}
		}
		if selected != "" && !anyTarget {
			return fmt.Errorf("Code notifyType %q resolves to no notification endpoints", selected)
		}
		if selected == "" && group == nil && (rule.EndpointRefs == nil || len(*rule.EndpointRefs) == 0) {
			if rule.Driver != nil {
				release, err := reserveRoutingScratch(scratch, len(rule.Driver.Config), 0)
				if err != nil {
					return err
				}
				defer release()
				if err := api.ValidateDriver("notification", *rule.Driver); err != nil {
					return err
				}
				if sink != nil {
					cloned := *rule.Driver
					cloned.Config = slices.Clone(cloned.Config)
					if cloned.CredentialRefs != nil {
						refs := maps.Clone(*cloned.CredentialRefs)
						cloned.CredentialRefs = &refs
					}
					sink.result.InlineDriver = &cloned
				}
			} else if rule.Dispatch != nil && *rule.Dispatch {
				return fmt.Errorf("dispatching Code requires a notification destination")
			}
		}
		return ctx.Err()
	}
	if rule.GroupRef == nil {
		return resolve(nil)
	}
	return withRoutingSpec(ctx, lookup, persistence.CatalogKey{Kind: "NotificationGroup", ID: *rule.GroupRef}, visit, scratch,
		func(meta api.Metadata, spec api.NotificationGroupSpec) error {
			sink.dependency("NotificationGroup", meta)
			return resolve(&spec)
		})
}

func withRoutingEndpoint(ctx context.Context, lookup resourceLookup, key persistence.CatalogKey, visit func() error, scratch reserveResourceScratch, fn func(api.Metadata, string) error) error {
	if authenticated, ok := lookup.(routingEndpointLookup); ok {
		if err := visitRouting(ctx, visit); err != nil {
			return err
		}
		found, err := authenticated.withRoutingEndpoint(ctx, key, fn)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("notification endpoint %q does not exist", key.ID)
		}
		return ctx.Err()
	}
	return withRoutingSpec(ctx, lookup, key, visit, scratch, func(meta api.Metadata, spec api.DriverConfig) error {
		return fn(meta, spec.Type)
	})
}

// withRoutingSpec keeps typed provider data and its reservation strictly inside
// the lookup's borrow callback. Result sinks may retain only identity fields.
func withRoutingSpec[T any](ctx context.Context, lookup resourceLookup, key persistence.CatalogKey, visit func() error, scratch reserveResourceScratch, fn func(api.Metadata, T) error) error {
	if err := visitRouting(ctx, visit); err != nil {
		return err
	}
	if lookup == nil {
		return ErrValidation
	}
	found, err := lookup.withResource(ctx, key, func(resource *api.Resource) error {
		if resource == nil || resource.Kind != key.Kind || resource.Metadata.ID != key.ID {
			return fmt.Errorf("%s %q has inconsistent identity", routingLabel(key.Kind), key.ID)
		}
		inputBytes, fields := len(resource.Spec)+len(resource.Status)+len(resource.APIVersion)+len(resource.Kind), 0
		inputBytes += len(resource.Metadata.ID) + len(resource.Metadata.UID) + len(resource.Metadata.ResourceVersion)
		if resource.Metadata.Name != nil {
			inputBytes += len(*resource.Metadata.Name)
		}
		if resource.Metadata.Labels != nil {
			fields = len(*resource.Metadata.Labels)
			for k, v := range *resource.Metadata.Labels {
				inputBytes += len(k) + len(v)
			}
		}
		release, err := reserveRoutingScratch(scratch, inputBytes, fields)
		if err != nil {
			return err
		}
		defer release()
		if err := api.ValidateResourceValue(*resource); err != nil {
			return fmt.Errorf("%s %q is invalid: %w", routingLabel(key.Kind), key.ID, err)
		}
		var spec T
		defer func() {
			if driver, ok := any(spec).(api.DriverConfig); ok {
				clear(driver.Config)
			}
		}()
		if err := api.StrictDecode(resource.Spec, &spec); err != nil {
			return ErrValidation
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return fn(resource.Metadata, spec)
	})
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%s %q does not exist", routingLabel(key.Kind), key.ID)
	}
	return ctx.Err()
}

func reserveRoutingScratch(reserve reserveResourceScratch, inputBytes, metadataFields int) (func(), error) {
	if reserve == nil {
		return func() {}, nil
	}
	if inputBytes < 0 || inputBytes > api.MaxResourceBytes || metadataFields < 0 || metadataFields > api.MaxResourceBytes {
		return nil, ErrGraphLimit
	}
	// Cover up to six-byte JSON escaping, validation/decode byte copies, and
	// typed string/slice/map bookkeeping with a 16-byte-per-input-byte allowance;
	// metadata maps additionally reserve 128 bytes per entry and a 4 KiB base.
	// This conservative logical quota is not a Go allocator/total-heap bound.
	// Check before multiplying: even on 32-bit targets these limits fit int.
	// Large individual resources may exceed a caller's smaller scratch budget.
	release, err := reserve(4096 + 16*inputBytes + 128*metadataFields)
	if err != nil {
		return nil, err
	}
	if release == nil {
		return nil, ErrValidation
	}
	return release, nil
}

func routingLabel(kind string) string {
	switch kind {
	case "NotificationEndpoint":
		return "notification endpoint"
	case "NotificationGroup":
		return "notification group"
	case "Recipient":
		return "recipient"
	default:
		return "notification resource"
	}
}

func visitRouting(ctx context.Context, visit func() error) error {
	if ctx == nil {
		return ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := visitNotification(visit); err != nil {
		return err
	}
	return ctx.Err()
}

func visitNotification(visit func() error) error {
	if visit != nil {
		return visit()
	}
	return nil
}

// notificationCatalogLookup adapts existing typed maps without materializing a
// second catalog. Only one newly encoded spec exists within each borrow callback.
type notificationCatalogLookup struct{ NotificationCatalog }

func (c notificationCatalogLookup) withResource(ctx context.Context, key persistence.CatalogKey, fn func(*api.Resource) error) (bool, error) {
	if ctx == nil || fn == nil {
		return false, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	var resource api.Resource
	var spec any
	switch key.Kind {
	case "NotificationEndpoint":
		value, ok := c.Endpoints[key.ID]
		if !ok {
			return false, nil
		}
		resource = api.Resource{APIVersion: value.APIVersion, Kind: value.Kind, Metadata: value.Metadata, Status: value.Status}
		spec = value.Spec
	case "Recipient":
		value, ok := c.Recipients[key.ID]
		if !ok {
			return false, nil
		}
		resource = api.Resource{APIVersion: value.APIVersion, Kind: value.Kind, Metadata: value.Metadata, Status: value.Status}
		spec = value.Spec
	case "NotificationGroup":
		value, ok := c.Groups[key.ID]
		if !ok {
			return false, nil
		}
		resource = api.Resource{APIVersion: value.APIVersion, Kind: value.Kind, Metadata: value.Metadata, Status: value.Status}
		spec = value.Spec
	default:
		return false, nil
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return true, err
	}
	defer clear(raw)
	resource.Spec = raw
	if err := ctx.Err(); err != nil {
		return true, err
	}
	if err := fn(&resource); err != nil {
		return true, err
	}
	return true, ctx.Err()
}

// ValidateNotificationGraph checks all shared definitions, including unreferenced
// ones, and affected monitors. This pure validation establishes no admission or
// catalog lock. Map callers retain their existing bounded graph ownership.
func ValidateNotificationGraph(catalog NotificationCatalog, monitors []api.Monitor) error {
	if err := validateNotificationDefinitions(catalog, nil); err != nil {
		return err
	}
	for _, monitor := range monitors {
		if err := validateNotificationMonitor(catalog, monitor, nil); err != nil {
			return err
		}
	}
	return nil
}

func validateNotificationDefinitions(catalog NotificationCatalog, visit func() error) error {
	lookup := notificationCatalogLookup{catalog}
	for _, keys := range []struct {
		kind string
		ids  []string
	}{
		{"NotificationEndpoint", slices.Sorted(maps.Keys(catalog.Endpoints))},
		{"Recipient", slices.Sorted(maps.Keys(catalog.Recipients))},
		{"NotificationGroup", slices.Sorted(maps.Keys(catalog.Groups))},
	} {
		for _, id := range keys.ids {
			if err := validateNotificationDefinitionLookup(context.Background(), persistence.CatalogKey{Kind: keys.kind, ID: id}, lookup, visit, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

func routingExists(ctx context.Context, lookup resourceLookup, key persistence.CatalogKey, visit func() error) (bool, error) {
	if err := visitRouting(ctx, visit); err != nil {
		return false, err
	}
	if lookup == nil {
		return false, ErrValidation
	}
	if authenticated, ok := lookup.(routingExistenceLookup); ok {
		return authenticated.routingExists(ctx, key)
	}
	found, err := lookup.withResource(ctx, key, func(*api.Resource) error { return nil })
	if err != nil {
		return found, err
	}
	return found, ctx.Err()
}

func validateNotificationDefinitionLookup(ctx context.Context, key persistence.CatalogKey, lookup resourceLookup, visit func() error, scratch reserveResourceScratch) error {
	checkReferences := func(refs []string, kind string) error {
		for _, ref := range refs {
			found, err := routingExists(ctx, lookup, persistence.CatalogKey{Kind: kind, ID: ref}, visit)
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("%s %q references missing %s %q", routingLabel(key.Kind), key.ID, routingLabel(kind), ref)
			}
		}
		return nil
	}
	switch key.Kind {
	case "NotificationEndpoint":
		return withRoutingEndpoint(ctx, lookup, key, visit, scratch, func(_ api.Metadata, _ string) error {
			return nil
		})
	case "Recipient":
		return withRoutingSpec(ctx, lookup, key, visit, scratch, func(_ api.Metadata, spec api.RecipientSpec) error {
			return checkReferences(spec.EndpointRefs, "NotificationEndpoint")
		})
	case "NotificationGroup":
		return withRoutingSpec(ctx, lookup, key, visit, scratch, func(_ api.Metadata, spec api.NotificationGroupSpec) error {
			if err := checkReferences(spec.EndpointRefs, "NotificationEndpoint"); err != nil {
				return err
			}
			return checkReferences(spec.RecipientRefs, "Recipient")
		})
	default:
		return ErrValidation
	}
}

func validateNotificationMonitor(catalog NotificationCatalog, monitor api.Monitor, visit func() error) error {
	return validateNotificationMonitorLookup(context.Background(), monitor, notificationCatalogLookup{catalog}, visit, nil)
}

func validateNotificationMonitorLookup(ctx context.Context, monitor api.Monitor, lookup resourceLookup, visit func() error, scratch reserveResourceScratch) error {
	if err := visitRouting(ctx, visit); err != nil {
		return err
	}
	if monitor.Spec.NotificationGroupRefs != nil {
		for _, ref := range *monitor.Spec.NotificationGroupRefs {
			found, err := routingExists(ctx, lookup, persistence.CatalogKey{Kind: "NotificationGroup", ID: ref}, visit)
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("monitor %q references missing notification group %q", monitor.Metadata.ID, ref)
			}
		}
	}
	if monitor.Spec.Notifications != nil {
		for _, color := range slices.Sorted(maps.Keys(*monitor.Spec.Notifications)) {
			if err := visitRouting(ctx, visit); err != nil {
				return err
			}
			if err := validateNotificationRuleLookup(ctx, (*monitor.Spec.Notifications)[color], lookup, visit, scratch); err != nil {
				return fmt.Errorf("monitor %q notifications.%s: %w", monitor.Metadata.ID, color, err)
			}
		}
	}
	return ctx.Err()
}
