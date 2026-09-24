package management

import (
	"context"
	"errors"
	"slices"

	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func (v *stagedValidator) expand(ctx context.Context) error {
	loaded := map[persistence.CatalogKey]bool{}
	var dependencies func(persistence.CatalogKey, int) error
	dependencies = func(key persistence.CatalogKey, cause int) error {
		if err := v.visit(ctx); err != nil {
			return err
		}
		if loaded[key] {
			return nil
		}
		if !v.options.CanRead(key) || !v.source.canRead(key) {
			return errCollectionReadDenied
		}
		if _, exists := v.cause[key]; !exists {
			v.cause[key] = cause
		}
		if _, exists := v.live[key]; !exists {
			if _, err := v.withLive(ctx, key, func(*api.Resource) error { return nil }); err != nil {
				return err
			}
		}
		old, live := v.live[key]
		desired, staged := v.desired[key]
		if !live && !staged {
			return persistence.ErrCatalogDependency
		}
		loaded[key] = true
		for _, version := range []stagedValidationVersion{old, desired} {
			for _, ref := range version.refs {
				if err := dependencies(ref, cause); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, key := range collectionKeys(v.desired) {
		if err := dependencies(key, v.item[key]); err != nil {
			_, err = v.reject(v.item[key], err)
			return err
		}
	}
	queue := []persistence.CatalogKey{}
	enqueued := map[persistence.CatalogKey]bool{}
	enqueue := func(key persistence.CatalogKey) error {
		if err := v.visit(ctx); err != nil {
			return err
		}
		if !enqueued[key] {
			enqueued[key] = true
			queue = append(queue, key)
		}
		return nil
	}
	for _, key := range collectionKeys(v.desired) {
		if _, exists := v.live[key]; exists && v.result.Items[v.item[key]].Change != "unchanged" {
			if err := enqueue(key); err != nil {
				_, err = v.reject(v.item[key], err)
				return err
			}
		}
	}
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		old := v.live[key]
		dependents, guard, err := v.catalog.store.CatalogDependents(key, maxValidationGraph)
		if err != nil || guard != old.dependentsVersion {
			switch {
			case errors.Is(err, persistence.ErrCatalogNotFound), err == nil:
				err = persistence.ErrCatalogConflict
			case !v.catalog.Ready():
				err = ErrUnavailable
			default:
				err = ErrGraphLimit
			}
			_, err = v.reject(v.cause[key], err)
			return err
		}
		v.reverseGuards[key] = guard
		for _, dependent := range dependents {
			if err := dependencies(dependent, v.cause[key]); err != nil {
				_, err = v.reject(v.cause[key], err)
				return err
			}
			if _, exists := v.live[dependent]; !exists {
				_, err = v.reject(v.cause[key], persistence.ErrCatalogConflict)
				return err
			}
			if _, included := v.item[dependent]; !included && !enqueued[dependent] {
				v.result.Impacted = append(v.result.Impacted, dependent)
			}
			if err := enqueue(dependent); err != nil {
				_, err = v.reject(v.cause[key], err)
				return err
			}
		}
	}
	slices.SortFunc(v.result.Impacted, collectionCompareKey)
	v.result.Impacted = slices.Compact(v.result.Impacted)
	return nil
}

func (v *stagedValidator) order(ctx context.Context) ([]persistence.CatalogKey, error) {
	seen := map[persistence.CatalogKey]int{}
	order := []persistence.CatalogKey{}
	var visit func(persistence.CatalogKey) error
	visit = func(key persistence.CatalogKey) error {
		if err := v.visit(ctx); err != nil {
			return err
		}
		if seen[key] == 1 {
			return ErrValidation
		}
		if seen[key] == 2 {
			return nil
		}
		seen[key] = 1
		for _, ref := range v.desired[key].refs {
			if _, included := v.desired[ref]; included {
				if err := visit(ref); err != nil {
					return err
				}
			}
		}
		seen[key] = 2
		order = append(order, key)
		return nil
	}
	keys := collectionKeys(v.desired)
	for _, kind := range ResourceKinds() {
		for _, key := range keys {
			if key.Kind == kind {
				if err := visit(key); err != nil {
					return nil, err
				}
			}
		}
	}
	return order, nil
}

func (v *stagedValidator) selectedRefs(key persistence.CatalogKey, desired bool) []persistence.CatalogKey {
	if desired {
		return v.desired[key].refs
	}
	return v.live[key].refs
}

func (v *stagedValidator) prefixes(ctx context.Context, order []persistence.CatalogKey) error {
	state := map[persistence.CatalogKey]bool{}
	reverse := map[persistence.CatalogKey]map[persistence.CatalogKey]bool{}
	updateEdges := func(key persistence.CatalogKey, desired, add bool) error {
		for _, ref := range v.selectedRefs(key, desired) {
			if err := v.visit(ctx); err != nil {
				return err
			}
			if add {
				if reverse[ref] == nil {
					reverse[ref] = map[persistence.CatalogKey]bool{}
				}
				reverse[ref][key] = true
			} else {
				delete(reverse[ref], key)
			}
		}
		return nil
	}
	for _, key := range collectionKeys(v.live) {
		state[key] = false
		if err := updateEdges(key, false, true); err != nil {
			_, err = v.reject(v.cause[key], err)
			return err
		}
	}
	for _, key := range order {
		if err := v.visit(ctx); err != nil {
			_, err = v.reject(v.item[key], err)
			return err
		}
		unchanged := v.result.Items[v.item[key]].Change == "unchanged"
		if unchanged && v.plan == nil {
			continue
		}
		if !unchanged {
			if old, exists := state[key]; exists {
				if err := updateEdges(key, old, false); err != nil {
					_, err = v.reject(v.item[key], err)
					return err
				}
			}
			state[key] = true
			if err := updateEdges(key, true, true); err != nil {
				_, err = v.reject(v.item[key], err)
				return err
			}
		}
		affected := map[persistence.CatalogKey]bool{key: true}
		var queue []persistence.CatalogKey
		if !unchanged {
			queue = []persistence.CatalogKey{key}
		}
		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]
			for _, dependent := range collectionKeys(reverse[current]) {
				if err := v.visit(ctx); err != nil {
					_, err = v.reject(v.item[key], err)
					return err
				}
				if !affected[dependent] {
					affected[dependent] = true
					queue = append(queue, dependent)
				}
			}
		}
		subset := map[persistence.CatalogKey]bool{}
		queued := map[persistence.CatalogKey]bool{}
		queue = collectionKeys(affected)
		for _, current := range queue {
			queued[current] = true
		}
		for len(queue) > 0 {
			if err := v.visit(ctx); err != nil {
				_, err = v.reject(v.item[key], err)
				return err
			}
			current := queue[0]
			queue = queue[1:]
			desired, exists := state[current]
			if !exists {
				v.result.Items[v.item[key]].Issue = "unsafePrefix"
				return ErrValidation
			}
			subset[current] = desired
			for _, ref := range v.selectedRefs(current, desired) {
				if err := v.visit(ctx); err != nil {
					_, err = v.reject(v.item[key], err)
					return err
				}
				if !queued[ref] {
					queued[ref] = true
					queue = append(queue, ref)
				}
			}
		}
		if _, err := v.validateSet(ctx, subset); err != nil {
			if !errors.Is(err, ErrValidation) && !errors.Is(err, persistence.ErrCatalogDependency) {
				_, err = v.reject(v.item[key], err)
				return err
			}
			v.result.Items[v.item[key]].Issue = "unsafePrefix"
			return ErrValidation
		}
		if v.plan != nil {
			if err := v.plan.prefix(ctx, v, key, subset, affected); err != nil {
				_, err = v.reject(v.item[key], err)
				return err
			}
		}
	}
	return nil
}

func (v *stagedValidator) validateSet(ctx context.Context, selected map[persistence.CatalogKey]bool) (persistence.CatalogKey, error) {
	lookup := stagedSelectedLookup{validator: v, selected: selected}
	for _, key := range collectionKeys(selected) {
		for _, ref := range v.selectedRefs(key, selected[key]) {
			if err := v.visit(ctx); err != nil {
				return key, err
			}
			if _, exists := selected[ref]; !exists {
				return key, persistence.ErrCatalogDependency
			}
		}
		found, err := lookup.withResource(ctx, key, func(r *api.Resource) error {
			release, err := reserveRoutingScratch(v.source.reserve, len(r.Spec)+len(r.Status), 0)
			if err != nil {
				return err
			}
			defer release()
			if validateMetadata(r.Metadata) != nil {
				return ErrValidation
			}
			return v.validateDriversAndMonitor(ctx, r, lookup)
		})
		if err != nil {
			return key, stagedValidationError(err)
		}
		if !found {
			return key, persistence.ErrCatalogDependency
		}
		if key.Kind == "NotificationEndpoint" || key.Kind == "Recipient" || key.Kind == "NotificationGroup" {
			if err := validateNotificationDefinitionLookup(ctx, key, lookup, func() error { return v.visit(ctx) }, v.source.reserve); err != nil {
				return key, stagedValidationError(err)
			}
		}
	}
	return persistence.CatalogKey{}, nil
}

// Validate resolved drivers without marshalling them back into the borrowed
// resource. Each resolution/output reservation lasts through its last use only.
func (v *stagedValidator) validateDriversAndMonitor(ctx context.Context, resource *api.Resource, lookup resourceLookup) error {
	driver := func(category string, input api.DriverConfig) error {
		defer clear(input.Config)
		if jobs.ValidateDriver(category, input.Type) != nil || validateProtectedDriver(category, input) != nil {
			return ErrValidation
		}
		release, err := resolveDriverWithLookup(ctx, &input, category, lookup, v.source.reserve)
		if err != nil {
			return stagedValidationError(err)
		}
		defer release()
		defer clear(input.Config)
		if err := ValidateResolvedDriver(category, input); err != nil {
			return ErrValidation
		}
		return ctx.Err()
	}
	switch resource.Kind {
	case "Credential":
		copy := *resource
		if validateDesired(&copy) != nil {
			return ErrValidation
		}
	case "NotificationEndpoint":
		var spec api.DriverConfig
		if api.StrictDecode(resource.Spec, &spec) != nil {
			return ErrValidation
		}
		return driver("notification", spec)
	case "Monitor":
		var spec api.MonitorSpec
		if api.StrictDecode(resource.Spec, &spec) != nil || ValidateMonitorSettings(spec) != nil {
			return ErrValidation
		}
		defer clear(spec.Check.Driver.Config)
		if spec.Recovery != nil {
			defer clear(spec.Recovery.Driver.Config)
		}
		if spec.Notifications != nil {
			for _, rule := range *spec.Notifications {
				if rule.Driver != nil {
					defer clear(rule.Driver.Config)
				}
			}
		}
		if err := driver("check", spec.Check.Driver); err != nil {
			return err
		}
		if spec.Recovery != nil {
			if err := driver("recovery", spec.Recovery.Driver); err != nil {
				return err
			}
		}
		if spec.Notifications != nil {
			for _, color := range collectionNotificationColors(*spec.Notifications) {
				rule := (*spec.Notifications)[color]
				if rule.Driver != nil {
					// The routing validation below also observes this inline DTO.
					copy := *rule.Driver
					copy.Config = slices.Clone(rule.Driver.Config)
					if err := driver("notification", copy); err != nil {
						return err
					}
				}
			}
		}
		return validateNotificationMonitorLookup(ctx, api.Monitor{APIVersion: resource.APIVersion, Kind: resource.Kind, Metadata: resource.Metadata, Spec: spec}, lookup, func() error { return v.visit(ctx) }, v.source.reserve)
	}
	return nil
}

func collectionNotificationColors(rules map[string]api.AlertRule) []string {
	colors := make([]string, 0, len(rules))
	for color := range rules {
		colors = append(colors, color)
	}
	slices.Sort(colors)
	return colors
}
