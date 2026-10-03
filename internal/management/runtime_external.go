//go:build externaljobs

package management

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type runtimePreparationExtensions struct {
	ExternalBindings []manifest.ExternalRuntimeBinding
}

// resolveRuntimeExtension selects only authenticated inert input. It does not
// construct a local job, admit an execution, or enable public configuration.
func (p *RuntimePreparation) resolveRuntimeExtension(ctx context.Context, resource api.Resource, record persistence.CatalogRecord, slot, category, driverType string) (any, bool, error) {
	if driverType != "external" {
		return nil, false, nil
	}
	bindings, err := externalRuntimeBindings(ctx, resource, record)
	if err != nil {
		return nil, true, err
	}
	for _, binding := range bindings {
		if binding.Key.Slot != slot || binding.Key.JobType.Category != category {
			continue
		}
		var marker any
		switch category {
		case "check":
			marker = &manifest.ExternalPulseConfig{Key: binding.Key}
		case "recovery":
			marker = &manifest.ExternalInterventionConfig{Key: binding.Key}
		case "notification":
			marker = &manifest.ExternalNotificationConfig{Key: binding.Key}
		default:
			return nil, true, ErrValidation
		}
		parameters := binding.Descriptor.Parameters()
		cost := len(parameters) + len(binding.Descriptor.CredentialProfile()) + 4096
		clear(parameters)
		if len(p.ExternalBindings) >= maxValidationGraph || cost > (32<<20)-p.PreparationBytes {
			return nil, true, ErrGraphLimit
		}
		p.PreparationBytes += cost
		p.ExternalBindings = append(p.ExternalBindings, binding)
		return marker, true, nil
	}
	return nil, true, ErrValidation
}

// Worker markers retain exact source revisions for later admission guards.
// Execution identity instead hashes configuration content and incarnations:
// changing a display name or rewrapping ciphertext must not reset an incident.
// This projection is private and contains no ciphertext, resource versions or
// mutable JobType metadata. The separate Conditions retain the exact revisions.
func (p RuntimePreparation) configurationRevision(ctx context.Context, resources map[persistence.CatalogKey]api.Resource, records map[persistence.CatalogKey]persistence.CatalogRecord) (string, error) {
	if len(p.ExternalBindings) == 0 {
		return manifest.ConfigurationRevision(p.Monitor, p.Endpoints, p.NotificationGroups)
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte("cpra-runtime-worker-content-v1\x00"))
	encoder := json.NewEncoder(hash)
	// Conditions have already been sorted by kind and ID. Retain numeric JSON
	// values exactly; converting custom parameters to float64 loses integers.
	for _, condition := range p.Conditions {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		resource, record := resources[condition.Key], records[condition.Key]
		var spec any
		decoder := json.NewDecoder(bytes.NewReader(resource.Spec))
		decoder.UseNumber()
		if err := decoder.Decode(&spec); err != nil {
			return "", ErrUnavailable
		}
		content := struct {
			Key      persistence.CatalogKey
			UID      string
			Spec     any
			JobTypes []persistence.JobTypeReference
		}{condition.Key, record.UID, spec, record.JobTypeReferences}
		if err := encoder.Encode(content); err != nil {
			return "", ErrUnavailable
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// externalRuntimeBindings converts an already authenticated Catalog.open result.
// It performs no storage reads, schema compilation, provider calls or admission.
// Callers must keep the resource and record immutable for the duration of this call.
func externalRuntimeBindings(ctx context.Context, resource api.Resource, record persistence.CatalogRecord) ([]manifest.ExternalRuntimeBinding, error) {
	if ctx == nil {
		return nil, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if resource.APIVersion != api.APIVersion || record.Removed || resource.Kind != record.Key.Kind || resource.Metadata.ID != record.Key.ID || resource.Metadata.UID != record.UID || resource.Metadata.ResourceVersion != record.Revision || resource.Metadata.Generation <= 0 || uint64(resource.Metadata.Generation) != record.Generation || len(resource.Spec) > 1<<20 || len(record.JobTypeReferences) > persistence.MaxCatalogJobTypeReferences {
		return nil, ErrValidation
	}
	refs, err := persistence.CanonicalJobTypeReferences(record.JobTypeReferences)
	if err != nil || !slices.Equal(refs, record.JobTypeReferences) {
		return nil, ErrValidation
	}
	type selector struct{ id, version, category string }
	bySelector := make(map[selector]persistence.JobTypeReference, len(refs))
	for _, ref := range refs {
		key := selector{ref.JobTypeID, ref.Version, ref.Category}
		if _, ok := bySelector[key]; ok {
			return nil, ErrValidation
		}
		bySelector[key] = ref
	}
	used := make(map[persistence.JobTypeReference]bool, len(refs))
	var out []manifest.ExternalRuntimeBinding
	bind := func(slot, category string, driver api.DriverConfig) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if driver.Type != "external" {
			return nil
		}
		config, err := externalDriverConfig(driver)
		if err != nil {
			return ErrValidation
		}
		ref, ok := bySelector[selector{config.JobTypeID, config.Version, category}]
		if !ok {
			return ErrValidation
		}
		identity := manifest.ExternalJobIdentity{JobTypeID: ref.JobTypeID, JobTypeUID: ref.JobTypeUID, Version: ref.Version, Revision: ref.Revision, Category: ref.Category}
		key := manifest.ExternalRuntimeKey{SourceKind: resource.Kind, SourceID: resource.Metadata.ID, SourceUID: record.UID, SourceRevision: record.Revision, Slot: slot, JobType: identity}
		if key.Validate() != nil {
			return ErrValidation
		}
		descriptor, err := manifest.NewExternalJobDescriptor(identity, config.Parameters, config.CredentialProfile)
		if err != nil {
			return ErrValidation
		}
		out = append(out, manifest.ExternalRuntimeBinding{Key: key, Descriptor: descriptor})
		used[ref] = true
		return nil
	}
	switch resource.Kind {
	case "Monitor":
		var spec api.MonitorSpec
		if api.StrictDecode(resource.Spec, &spec) != nil || ValidateMonitorSettings(spec) != nil {
			return nil, ErrValidation
		}
		if err := bind("check", "check", spec.Check.Driver); err != nil {
			return nil, err
		}
		if spec.Recovery != nil {
			if err := bind("recovery", "recovery", spec.Recovery.Driver); err != nil {
				return nil, err
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
					if err := bind("notifications."+color, "notification", *rule.Driver); err != nil {
						return nil, err
					}
				}
			}
		}
	case "NotificationEndpoint":
		var driver api.DriverConfig
		if api.StrictDecode(resource.Spec, &driver) != nil {
			return nil, ErrValidation
		}
		if err := bind("endpoint", "notification", driver); err != nil {
			return nil, err
		}
	default:
		return nil, ErrValidation
	}
	if len(used) != len(refs) {
		return nil, ErrValidation
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
