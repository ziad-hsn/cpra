package management

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type validationGraph struct {
	resources map[persistence.CatalogKey]api.Resource
	records   map[persistence.CatalogKey]persistence.CatalogRecord
	bytes     int
}

// validateGraph builds only the affected dependency closure. The reverse-edge
// guard rejects a concurrently added/edited dependent at Apply, and conditions
// freeze every other read used to justify the mutation.
func (c *Catalog) validateGraph(ctx context.Context, view persistence.CatalogView, candidate api.Resource, old persistence.CatalogRecord, create bool) ([]persistence.CatalogKey, []persistence.CatalogCondition, error) {
	key := persistence.CatalogKey{Kind: candidate.Kind, ID: candidate.Metadata.ID}
	graph := validationGraph{resources: map[persistence.CatalogKey]api.Resource{key: candidate}, records: map[persistence.CatalogKey]persistence.CatalogRecord{}}
	visiting := make(map[persistence.CatalogKey]bool)
	var load func(persistence.CatalogKey) error
	load = func(ref persistence.CatalogKey) error {
		if visiting[ref] {
			return ErrValidation
		}
		if _, ok := graph.resources[ref]; ok {
			return nil
		}
		if len(graph.resources) >= maxValidationGraph {
			return ErrGraphLimit
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		record, exists := view.Get(ref)
		if !exists {
			return persistence.ErrCatalogDependency
		}
		graph.bytes += len(record.Payload.Ciphertext)
		if graph.bytes > 32<<20 {
			return ErrGraphLimit
		}
		resource, err := c.open(ctx, record)
		if err != nil {
			return err
		}
		visiting[ref] = true
		refs, err := directReferences(resource)
		if err != nil {
			return err
		}
		for _, child := range refs {
			if err := load(child); err != nil {
				return err
			}
		}
		delete(visiting, ref)
		graph.resources[ref], graph.records[ref] = resource, record
		return nil
	}
	refs, err := directReferences(candidate)
	if err != nil {
		return nil, nil, err
	}
	for _, ref := range refs {
		if err := load(ref); err != nil {
			return nil, nil, err
		}
	}
	// Persist only actual direct edges. A cached transitive edge would become
	// stale after a shared-resource edit and could both miss invalidation and
	// incorrectly prevent deletion of a credential no longer used by a monitor.
	allRefs := slices.Clone(refs)
	guards := make(map[persistence.CatalogKey]uint64)
	if !create {
		queue := []persistence.CatalogKey{key}
		seen := map[persistence.CatalogKey]bool{key: true}
		for len(queue) != 0 {
			current := queue[0]
			queue = queue[1:]
			observed := old
			if current != key {
				observed = graph.records[current]
			}
			dependents, guard, err := c.store.CatalogDependents(current, maxValidationGraph)
			if err != nil {
				return nil, nil, err
			}
			if guard != observed.DependentsVersion {
				return nil, nil, persistence.ErrCatalogConflict
			}
			if current != key {
				guards[current] = guard
			}
			for _, dependent := range dependents {
				if seen[dependent] {
					continue
				}
				if err := load(dependent); err != nil {
					return nil, nil, err
				}
				seen[dependent] = true
				queue = append(queue, dependent)
			}
		}
	}
	notifications := NotificationCatalog{Endpoints: map[string]api.NotificationEndpoint{}, Recipients: map[string]api.Recipient{}, Groups: map[string]api.NotificationGroup{}}
	monitors := []api.Monitor{}
	for _, resource := range graph.resources {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		raw, _ := json.Marshal(resource)
		switch resource.Kind {
		case "Monitor":
			var monitor api.Monitor
			if json.Unmarshal(raw, &monitor) != nil {
				return nil, nil, ErrUnavailable
			}
			if err := ValidateMonitorSettings(monitor.Spec); err != nil {
				return nil, nil, err
			}
			monitors = append(monitors, monitor)
		case "NotificationEndpoint":
			var endpoint api.NotificationEndpoint
			if json.Unmarshal(raw, &endpoint) != nil {
				return nil, nil, ErrUnavailable
			}
			notifications.Endpoints[resource.Metadata.ID] = endpoint
		case "Recipient":
			var recipient api.Recipient
			if json.Unmarshal(raw, &recipient) != nil {
				return nil, nil, ErrUnavailable
			}
			notifications.Recipients[resource.Metadata.ID] = recipient
		case "NotificationGroup":
			var group api.NotificationGroup
			if json.Unmarshal(raw, &group) != nil {
				return nil, nil, ErrUnavailable
			}
			notifications.Groups[resource.Metadata.ID] = group
		}
		// Resolve credentials into a short-lived copy for type validation. The
		// copy is never returned, retained in PreparedChange, or sent to Raft.
		if err := visitDrivers(&resource, func(category string, driver *api.DriverConfig) error {
			if err := resolveDriver(driver, category, graph.resources); err != nil {
				return err
			}
			return ValidateResolvedDriver(category, *driver)
		}); err != nil {
			return nil, nil, err
		}
	}
	if err := ValidateNotificationGraph(notifications, monitors); err != nil {
		// Routing diagnostics contain identities/types, never driver values.
		return nil, nil, errors.Join(ErrValidation, err)
	}
	conditions := make([]persistence.CatalogCondition, 0, len(graph.records))
	for ref, record := range graph.records {
		condition := persistence.CatalogCondition{Key: ref, UID: record.UID, Revision: record.Revision}
		if guard, ok := guards[ref]; ok {
			condition.ExpectedDependentsVersion = &guard
		}
		conditions = append(conditions, condition)
	}
	less := func(a, b persistence.CatalogKey) int {
		if n := strings.Compare(a.Kind, b.Kind); n != 0 {
			return n
		}
		return strings.Compare(a.ID, b.ID)
	}
	slices.SortFunc(allRefs, less)
	slices.SortFunc(conditions, func(a, b persistence.CatalogCondition) int { return less(a.Key, b.Key) })
	return allRefs, conditions, nil
}

func directReferences(resource api.Resource) ([]persistence.CatalogKey, error) {
	refs := make(map[persistence.CatalogKey]bool)
	add := func(kind string, ids ...string) error {
		for _, id := range ids {
			if !validID(id) {
				return ErrValidation
			}
			refs[persistence.CatalogKey{Kind: kind, ID: id}] = true
		}
		return nil
	}
	switch resource.Kind {
	case "NotificationGroup":
		var spec api.NotificationGroupSpec
		if api.StrictDecode(resource.Spec, &spec) != nil {
			return nil, ErrValidation
		}
		if err := add("NotificationEndpoint", spec.EndpointRefs...); err != nil {
			return nil, err
		}
		if err := add("Recipient", spec.RecipientRefs...); err != nil {
			return nil, err
		}
	case "Recipient":
		var spec api.RecipientSpec
		if api.StrictDecode(resource.Spec, &spec) != nil {
			return nil, ErrValidation
		}
		if err := add("NotificationEndpoint", spec.EndpointRefs...); err != nil {
			return nil, err
		}
	case "Monitor":
		var spec api.MonitorSpec
		if api.StrictDecode(resource.Spec, &spec) != nil {
			return nil, ErrValidation
		}
		if spec.NotificationGroupRefs != nil {
			if err := add("NotificationGroup", (*spec.NotificationGroupRefs)...); err != nil {
				return nil, err
			}
		}
		if spec.Notifications != nil {
			for _, rule := range *spec.Notifications {
				if rule.GroupRef != nil {
					if err := add("NotificationGroup", *rule.GroupRef); err != nil {
						return nil, err
					}
				}
				if rule.EndpointRefs != nil {
					if err := add("NotificationEndpoint", (*rule.EndpointRefs)...); err != nil {
						return nil, err
					}
				}
				if rule.RecipientRefs != nil {
					if err := add("Recipient", (*rule.RecipientRefs)...); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	if err := visitDrivers(&resource, func(_ string, driver *api.DriverConfig) error {
		if driver.CredentialRefs != nil {
			for _, id := range *driver.CredentialRefs {
				if err := add("Credential", id); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	out := make([]persistence.CatalogKey, 0, len(refs))
	for ref := range refs {
		out = append(out, ref)
	}
	return out, nil
}

func resolveDriver(driver *api.DriverConfig, category string, resources map[persistence.CatalogKey]api.Resource) error {
	release, err := resolveDriverWithLookup(context.Background(), driver, category, mapResourceLookup(resources), nil)
	if release != nil {
		release()
	}
	return err
}
