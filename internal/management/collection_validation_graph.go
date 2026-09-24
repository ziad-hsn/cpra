package management

import (
	"context"
	"errors"
	"maps"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func (v *collectionValidator) validateSet(ctx context.Context, resources map[persistence.CatalogKey]api.Resource) (persistence.CatalogKey, error) {
	notifications := NotificationCatalog{Endpoints: map[string]api.NotificationEndpoint{}, Recipients: map[string]api.Recipient{}, Groups: map[string]api.NotificationGroup{}}
	monitors := []api.Monitor{}
	keys := collectionKeys(resources)
	for _, key := range keys {
		if err := v.visit(ctx); err != nil {
			return key, err
		}
		r := resources[key]
		if validateMetadata(r.Metadata) != nil || validateDesired(&r) != nil {
			return key, ErrValidation
		}
		refs, err := directReferences(r)
		if err != nil {
			return key, ErrValidation
		}
		for _, ref := range refs {
			if _, exists := resources[ref]; !exists {
				return key, persistence.ErrCatalogDependency
			}
		}
		switch r.Kind {
		case "Monitor":
			var spec api.MonitorSpec
			if api.StrictDecode(r.Spec, &spec) != nil || ValidateMonitorSettings(spec) != nil {
				return key, ErrValidation
			}
			monitors = append(monitors, api.Monitor{APIVersion: r.APIVersion, Kind: r.Kind, Metadata: r.Metadata, Spec: spec})
		case "NotificationEndpoint":
			var spec api.DriverConfig
			if api.StrictDecode(r.Spec, &spec) != nil {
				return key, ErrValidation
			}
			notifications.Endpoints[key.ID] = api.NotificationEndpoint{APIVersion: r.APIVersion, Kind: r.Kind, Metadata: r.Metadata, Spec: spec}
		case "Recipient":
			var spec api.RecipientSpec
			if api.StrictDecode(r.Spec, &spec) != nil {
				return key, ErrValidation
			}
			notifications.Recipients[key.ID] = api.Recipient{APIVersion: r.APIVersion, Kind: r.Kind, Metadata: r.Metadata, Spec: spec}
		case "NotificationGroup":
			var spec api.NotificationGroupSpec
			if api.StrictDecode(r.Spec, &spec) != nil {
				return key, ErrValidation
			}
			notifications.Groups[key.ID] = api.NotificationGroup{APIVersion: r.APIVersion, Kind: r.Kind, Metadata: r.Metadata, Spec: spec}
		}
		if err := visitDrivers(&r, func(category string, driver *api.DriverConfig) error {
			if err := resolveDriver(driver, category, resources); err != nil {
				return err
			}
			return ValidateResolvedDriver(category, *driver)
		}); err != nil {
			return key, ErrValidation
		}
	}
	// Validate shared definitions once, then each monitor exactly once. The
	// visitor bounds expanded routing too (e.g. one large group used by many
	// monitors), and cancellation remains observable within that expansion.
	visit := func() error { return v.visit(ctx) }
	if err := validateNotificationDefinitions(notifications, visit); err != nil {
		if len(keys) > 0 {
			return keys[0], collectionRoutingError(err)
		}
		return persistence.CatalogKey{}, collectionRoutingError(err)
	}
	for _, monitor := range monitors {
		if err := validateNotificationMonitor(notifications, monitor, visit); err != nil {
			return persistence.CatalogKey{Kind: "Monitor", ID: monitor.Metadata.ID}, collectionRoutingError(err)
		}
	}
	return persistence.CatalogKey{}, nil
}

// prefixes simulates only desired data in memory. The reverse adjacency is
// updated for each confirmed-in-this-simulation prefix, so old consumers remain
// present until their own turn. Only affected consumers plus their dependencies
// are validated per step; the total repeated visits also have a hard bound.
func (v *collectionValidator) prefixes(ctx context.Context, order []persistence.CatalogKey) error {
	state := maps.Clone(v.live)
	reverse := map[persistence.CatalogKey]map[persistence.CatalogKey]bool{}
	updateEdges := func(key persistence.CatalogKey, r api.Resource, add bool) error {
		refs, err := directReferences(r)
		if err != nil {
			return ErrValidation
		}
		for _, ref := range refs {
			if reverse[ref] == nil {
				reverse[ref] = map[persistence.CatalogKey]bool{}
			}
			if add {
				reverse[ref][key] = true
			} else {
				delete(reverse[ref], key)
			}
		}
		return nil
	}
	for _, key := range collectionKeys(state) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := updateEdges(key, state[key], true); err != nil {
			return err
		}
	}
	for _, key := range order {
		if err := ctx.Err(); err != nil {
			return err
		}
		if v.result.Items[v.item[key]].Change == "unchanged" {
			continue
		}
		if old, ok := state[key]; ok {
			if err := updateEdges(key, old, false); err != nil {
				return err
			}
		}
		state[key] = v.desired[key]
		if err := updateEdges(key, state[key], true); err != nil {
			return err
		}
		affected := map[persistence.CatalogKey]bool{}
		queue := []persistence.CatalogKey{key}
		for len(queue) > 0 {
			if err := v.visit(ctx); err != nil {
				_, err = v.reject(v.item[key], err)
				return err
			}
			current := queue[0]
			queue = queue[1:]
			if affected[current] {
				continue
			}
			affected[current] = true
			for _, dependent := range collectionKeys(reverse[current]) {
				queue = append(queue, dependent)
			}
		}
		subset := map[persistence.CatalogKey]api.Resource{}
		queue = collectionKeys(affected)
		for len(queue) > 0 {
			if err := v.visit(ctx); err != nil {
				_, err = v.reject(v.item[key], err)
				return err
			}
			current := queue[0]
			queue = queue[1:]
			if _, seen := subset[current]; seen {
				continue
			}
			r, exists := state[current]
			if !exists {
				v.result.Items[v.item[key]].Issue = "unsafePrefix"
				return ErrValidation
			}
			subset[current] = r
			refs, err := directReferences(r)
			if err != nil {
				return ErrValidation
			}
			queue = append(queue, refs...)
		}
		if _, err := v.validateSet(ctx, subset); err != nil {
			if err == ErrGraphLimit || ctx.Err() != nil {
				_, err = v.reject(v.item[key], err)
				return err
			}
			v.result.Items[v.item[key]].Issue = "unsafePrefix"
			return ErrValidation
		}
	}
	return nil
}

func (v *collectionValidator) visit(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	v.visits++
	if v.visits > collectionValidationVisits {
		return ErrGraphLimit
	}
	return nil
}

// Routing helpers wrap identity diagnostics for ordinary callers. Collection
// errors preserve only safe classifications and cancellation/limit identity.
func collectionRoutingError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, ErrGraphLimit):
		return ErrGraphLimit
	default:
		return ErrValidation
	}
}
