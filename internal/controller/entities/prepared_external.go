//go:build externaljobs

package entities

import (
	"errors"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/manifest"
)

const (
	maxExternalPreparationBindings = 10000
	maxExternalPreparationBytes    = 32 << 20
)

var errExternalPreparation = errors.New("invalid external monitor preparation")

func validateLocalJobPreparation(m manifest.Monitor, endpoints map[string]manifest.Endpoint, groups manifest.NotificationGroups) error {
	if _, ok := m.Pulse.Config.(*manifest.ExternalPulseConfig); ok {
		return errExternalPreparation
	}
	if _, ok := m.Intervention.Target.(*manifest.ExternalInterventionConfig); ok {
		return errExternalPreparation
	}
	for _, code := range m.Codes {
		if _, ok := code.Config.(*manifest.ExternalNotificationConfig); ok {
			return errExternalPreparation
		}
		for _, name := range groups[code.NotifyGroup] {
			if _, ok := endpoints[name].Config.(*manifest.ExternalNotificationConfig); ok {
				return errExternalPreparation
			}
		}
	}
	return nil
}

// PrepareMonitorExternal freezes explicitly supplied inert bindings alongside
// local job templates. It neither enables driver admission nor grants execution.
// The caller authenticates source/JobType records before preparation and owns the
// admission guards. Inputs may not be mutated concurrently with this call.
func PrepareMonitorExternal(m manifest.Monitor, endpoints map[string]manifest.Endpoint, groups manifest.NotificationGroups, bindings []manifest.ExternalRuntimeBinding) (*PreparedMonitor, error) {
	if len(bindings) > maxExternalPreparationBindings {
		return nil, errExternalPreparation
	}
	id, err := m.EffectiveID()
	if err != nil {
		return nil, err
	}
	p := &externalJobPreparer{
		monitorID: id,
		bindings:  make(map[manifest.ExternalRuntimeKey]*manifest.ExternalRuntimeBinding, len(bindings)),
		used:      make(map[manifest.ExternalRuntimeKey]bool, len(bindings)),
		storage:   components.ExternalJobStorage{Notifications: make(map[string][]*manifest.ExternalRuntimeBinding)},
	}
	// A source revision binds every slot of that resource to the same immutable
	// encrypted configuration. Conflicting incarnations cannot share preparation.
	type sourceKey struct{ kind, id string }
	type sourceVersion struct{ uid, revision string }
	sources := make(map[sourceKey]sourceVersion)
	for _, b := range bindings {
		if b.Key.Validate() != nil || b.Key.JobType != b.Descriptor.Identity() || p.bindings[b.Key] != nil {
			return nil, errExternalPreparation
		}
		if b.Key.SourceKind == "Monitor" && b.Key.SourceID != id {
			return nil, errExternalPreparation
		}
		source := sourceKey{b.Key.SourceKind, b.Key.SourceID}
		version := sourceVersion{b.Key.SourceUID, b.Key.SourceRevision}
		if old, exists := sources[source]; exists && old != version {
			return nil, errExternalPreparation
		}
		sources[source] = version
		parameters := b.Descriptor.Parameters()
		p.bytes += len(parameters) + len(b.Descriptor.CredentialProfile()) + 4096
		clear(parameters)
		if p.bytes > maxExternalPreparationBytes {
			return nil, errExternalPreparation
		}
		cloned := b.Clone()
		p.bindings[b.Key] = &cloned
	}
	return prepareMonitor(m, endpoints, groups, p)
}

type externalJobPreparer struct {
	localJobPreparer
	monitorID string
	bindings  map[manifest.ExternalRuntimeKey]*manifest.ExternalRuntimeBinding
	used      map[manifest.ExternalRuntimeKey]bool
	storage   components.ExternalJobStorage
	bytes     int
}

func (p *externalJobPreparer) binding(key manifest.ExternalRuntimeKey, kind, id, slot string) (*manifest.ExternalRuntimeBinding, error) {
	b := p.bindings[key]
	if b == nil || key.SourceKind != kind || key.SourceID != id || key.Slot != slot {
		return nil, errExternalPreparation
	}
	p.used[key] = true
	return b, nil
}

func (p *externalJobPreparer) pulse(config manifest.Pulse) (jobs.Job, error) {
	marker, external := config.Config.(*manifest.ExternalPulseConfig)
	if !external {
		return p.localJobPreparer.pulse(config)
	}
	if marker == nil || config.Type != "external" {
		return nil, errExternalPreparation
	}
	b, err := p.binding(marker.Key, "Monitor", p.monitorID, "check")
	if err != nil {
		return nil, err
	}
	p.storage.Check = b
	return nil, nil
}

func (p *externalJobPreparer) intervention(config manifest.Intervention) (jobs.Job, error) {
	marker, external := config.Target.(*manifest.ExternalInterventionConfig)
	if !external {
		return p.localJobPreparer.intervention(config)
	}
	if marker == nil || config.Action != "external" {
		return nil, errExternalPreparation
	}
	b, err := p.binding(marker.Key, "Monitor", p.monitorID, "recovery")
	if err != nil {
		return nil, err
	}
	p.storage.Recovery = b
	return nil, nil
}

func (p *externalJobPreparer) codes(monitor string, config manifest.CodeConfig, color string, endpoints map[string]manifest.Endpoint, groups manifest.NotificationGroups) ([]jobs.Job, error) {
	if config.NotifyGroup == "" {
		job, binding, err := p.notification(monitor, color, config, "Monitor", p.monitorID, "notifications."+color)
		if err != nil {
			return nil, err
		}
		p.storage.Notifications[color] = []*manifest.ExternalRuntimeBinding{binding}
		return []jobs.Job{job}, nil
	}
	if _, ok := config.Config.(*manifest.ExternalNotificationConfig); ok {
		return nil, errExternalPreparation // A group cannot also claim an inline slot.
	}
	names := groups[config.NotifyGroup]
	if len(names) == 0 || len(names) > maxExternalPreparationBindings {
		return nil, errExternalPreparation
	}
	// Charge slots before allocating either aligned slice. Bindings themselves
	// are copied once even when a destination appears in several colors.
	p.bytes += len(names) * 32
	if p.bytes > maxExternalPreparationBytes {
		return nil, errExternalPreparation
	}
	local := make([]jobs.Job, len(names))
	external := make([]*manifest.ExternalRuntimeBinding, len(names))
	for i, name := range names {
		ep, ok := endpoints[name]
		if !ok {
			return nil, errExternalPreparation
		}
		inline := manifest.CodeConfig{Dispatch: config.Dispatch, Notify: ep.Type, Config: ep.Config}
		job, binding, err := p.notification(monitor, color, inline, "NotificationEndpoint", name, "endpoint")
		if err != nil {
			return nil, err
		}
		local[i], external[i] = job, binding
	}
	p.storage.Notifications[color] = external
	return local, nil
}

func (p *externalJobPreparer) notification(monitor, color string, config manifest.CodeConfig, kind, id, slot string) (jobs.Job, *manifest.ExternalRuntimeBinding, error) {
	marker, external := config.Config.(*manifest.ExternalNotificationConfig)
	if !external {
		job, err := jobs.CreateCodeJob(monitor, config, ecs.Entity{}, color)
		return job, nil, err
	}
	if marker == nil || config.Notify != "external" {
		return nil, nil, errExternalPreparation
	}
	b, err := p.binding(marker.Key, kind, id, slot)
	return nil, b, err
}

func (p *externalJobPreparer) finish(storage *components.JobStorage) error {
	if len(p.used) != len(p.bindings) {
		return errExternalPreparation
	}
	if len(p.bindings) != 0 {
		storage.ExternalJobs = &p.storage
	}
	return nil
}
