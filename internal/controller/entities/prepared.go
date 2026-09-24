package entities

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/manifest"
)

// PreparedMonitor owns private configuration and job templates. Preparation does
// not touch an Ark world or execute providers. Hand it to the world owner for one
// Install or Replace; Close discards an unused preparation. Do not copy this value.
// Callers must not mutate the source while PrepareMonitor is copying it.
type PreparedMonitor struct {
	mu       sync.Mutex
	id       string
	revision string
	bundle   *preparedComponents
}

type preparedComponents struct {
	name         string
	enabled      bool
	maintenance  []manifest.CompiledWindow
	pulse        components.PulseConfig
	intervention *components.InterventionConfig
	codes        *components.CodeConfig
	jobs         components.JobStorage
}

func (p *PreparedMonitor) MonitorID() string { return p.id }
func (p *PreparedMonitor) Revision() string  { return p.revision }
func (p *PreparedMonitor) String() string    { return "PreparedMonitor{private configuration}" }
func (p *PreparedMonitor) GoString() string  { return p.String() }
func (p *PreparedMonitor) MarshalJSON() ([]byte, error) {
	return nil, errors.New("prepared monitor configuration cannot be serialized")
}

// Close releases unused private templates. Built-in constructors allocate no
// exclusive persistent clients; HTTP transports are shared pools and must not be
// closed here. After transfer this method is harmless and cannot close live jobs.
func (p *PreparedMonitor) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bundle = nil
	return nil
}

// PrepareMonitor freezes only this monitor's inputs and referenced destinations.
// All fallible configuration and constructor work occurs before entity allocation.
func PrepareMonitor(m manifest.Monitor, endpoints map[string]manifest.Endpoint, groups manifest.NotificationGroups) (*PreparedMonitor, error) {
	if err := validateLocalJobPreparation(m, endpoints, groups); err != nil {
		return nil, err
	}
	return prepareMonitor(m, endpoints, groups, localJobPreparer{})
}

func prepareMonitor(m manifest.Monitor, endpoints map[string]manifest.Endpoint, groups manifest.NotificationGroups, preparer monitorJobPreparer) (*PreparedMonitor, error) {
	if m.Name == "" {
		return nil, errors.New("monitor name cannot be empty")
	}
	id, err := m.EffectiveID()
	if err != nil {
		return nil, err
	}
	if nilConfig(m.Pulse.Config) {
		return nil, errors.New("pulse configuration is required")
	}
	m.Pulse.Config = m.Pulse.Config.Copy()
	m.Pulse.Groups = slices.Clone(m.Pulse.Groups)
	m.Tags = slices.Clone(m.Tags)
	m.Maintenance = slices.Clone(m.Maintenance)
	if m.Intervention.Action != "" {
		if nilConfig(m.Intervention.Target) {
			return nil, errors.New("intervention target is required")
		}
		// The legacy Docker constructor assumes the matching concrete target.
		if m.Intervention.Action == "docker" {
			if _, ok := m.Intervention.Target.(*manifest.InterventionTargetDocker); !ok {
				return nil, errors.New("docker intervention requires a docker target")
			}
		}
		m.Intervention.Target = m.Intervention.Target.Copy()
	} else {
		m.Intervention.Target = nil
	}
	var codes manifest.Codes
	if m.Codes != nil {
		codes = make(manifest.Codes, len(m.Codes))
	}
	privateEndpoints := make(map[string]manifest.Endpoint)
	privateGroups := make(manifest.NotificationGroups)
	for color, code := range m.Codes {
		if !nilConfig(code.Config) {
			code.Config = code.Config.Copy()
		} else {
			code.Config = nil
		}
		codes[color] = code
		if code.NotifyGroup == "" {
			continue
		}
		if _, exists := privateGroups[code.NotifyGroup]; exists {
			continue
		}
		names, exists := groups[code.NotifyGroup]
		if !exists || len(names) == 0 {
			return nil, errors.New("notification group is missing or has no endpoints")
		}
		privateGroups[code.NotifyGroup] = slices.Clone(names)
		for _, name := range names {
			if _, exists := privateEndpoints[name]; exists {
				continue
			}
			endpoint, exists := endpoints[name]
			if !exists || nilConfig(endpoint.Config) {
				return nil, errors.New("notification endpoint is missing or has no configuration")
			}
			endpoint.Config = endpoint.Config.Copy()
			privateEndpoints[name] = endpoint
		}
	}
	m.Codes = codes
	revision, err := manifest.ConfigurationRevision(m, privateEndpoints, privateGroups)
	if err != nil {
		return nil, err
	}
	windows, err := manifest.CompileMaintenance(m.Maintenance)
	if err != nil {
		return nil, err
	}
	b := &preparedComponents{
		name: m.Name, enabled: m.Enabled, maintenance: windows,
		pulse: components.PulseConfig{
			Type: m.Pulse.Type, Config: m.Pulse.Config, Timeout: m.Pulse.Timeout,
			Interval: m.Pulse.Interval, Retries: m.Pulse.Retries, UnhealthyThreshold: m.Pulse.UnhealthyThreshold,
			HealthyThreshold: m.Pulse.HealthyThreshold,
		},
		jobs: components.JobStorage{CodeJobs: make(map[string][]jobs.Job, len(codes))},
	}
	b.jobs.PulseJob, err = preparer.pulse(m.Pulse)
	if err != nil {
		return nil, fmt.Errorf("prepare pulse: %w", err)
	}
	if m.Intervention.Action != "" {
		b.intervention = &components.InterventionConfig{
			Action: m.Intervention.Action, Target: m.Intervention.Target,
			MaxFailures: max(1, m.Intervention.MaxFailures),
		}
		b.jobs.InterventionJob, err = preparer.intervention(m.Intervention)
		if err != nil {
			return nil, fmt.Errorf("prepare intervention: %w", err)
		}
	}
	if len(codes) > 0 {
		b.codes = &components.CodeConfig{Configs: make(map[string]*components.ColorCodeConfig, len(codes))}
	}
	// Stable constructor order makes failures reproducible even when colors arrive
	// through a Go map. Endpoint order within each group remains unchanged.
	colors := make([]string, 0, len(codes))
	for color := range codes {
		colors = append(colors, color)
	}
	slices.Sort(colors)
	for _, color := range colors {
		code := codes[color]
		b.codes.Configs[color] = &components.ColorCodeConfig{Dispatch: code.Dispatch, Notify: code.Notify, Config: code.Config}
		if !code.Dispatch && code.Notify == "" && code.NotifyGroup == "" && code.Config == nil {
			continue // An explicitly inert rule has no job to construct.
		}
		b.jobs.CodeJobs[color], err = preparer.codes(m.Name, code, color, privateEndpoints, privateGroups)
		if err != nil {
			return nil, fmt.Errorf("prepare notification: %w", err)
		}
	}
	if err := preparer.finish(&b.jobs); err != nil {
		return nil, err
	}
	return &PreparedMonitor{id: id, revision: revision, bundle: b}, nil
}

// The default preparer constructs only local jobs. Optional preparers may retain
// inert descriptors in the storage component, never a job that waits remotely.
type monitorJobPreparer interface {
	pulse(manifest.Pulse) (jobs.Job, error)
	intervention(manifest.Intervention) (jobs.Job, error)
	codes(string, manifest.CodeConfig, string, map[string]manifest.Endpoint, manifest.NotificationGroups) ([]jobs.Job, error)
	finish(*components.JobStorage) error
}

type localJobPreparer struct{}

func (localJobPreparer) pulse(config manifest.Pulse) (jobs.Job, error) {
	return jobs.CreatePulseJob(config, ecs.Entity{})
}
func (localJobPreparer) intervention(config manifest.Intervention) (jobs.Job, error) {
	return jobs.CreateInterventionJob(config, ecs.Entity{})
}
func (localJobPreparer) codes(monitor string, config manifest.CodeConfig, color string, endpoints map[string]manifest.Endpoint, groups manifest.NotificationGroups) ([]jobs.Job, error) {
	return jobs.CreateCodeJobs(monitor, config, ecs.Entity{}, color, endpoints, groups)
}
func (localJobPreparer) finish(*components.JobStorage) error { return nil }

func nilConfig(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	return v.Kind() == reflect.Pointer && v.IsNil()
}

// entityJob preserves legacy direct Execute identity without changing every
// optional driver. Dispatch remains authoritative for execution/revision IDs and
// overwrites Ent itself. This wrapper never wraps a Dispatch or another wrapper.
type entityJob struct {
	jobs.Job
	entity ecs.Entity
}

func (j *entityJob) Execute() jobs.Result {
	r := j.Job.Execute()
	r.Ent = j.entity
	return r
}
func (j *entityJob) IsNil() bool    { return j == nil || j.Job == nil || j.Job.IsNil() }
func (j *entityJob) Copy() jobs.Job { return &entityJob{Job: j.Job.Copy(), entity: j.entity} }
func (j *entityJob) SetContext(ctx context.Context) {
	if job, ok := j.Job.(interface{ SetContext(context.Context) }); ok {
		job.SetContext(ctx)
	}
}
func (j *entityJob) TracksEnqueueTime() bool {
	if job, ok := j.Job.(jobs.EnqueueTimeTracker); ok {
		return job.TracksEnqueueTime()
	}
	return true
}

func bindJobs(storage components.JobStorage, entity ecs.Entity) components.JobStorage {
	if storage.PulseJob != nil {
		storage.PulseJob = &entityJob{Job: storage.PulseJob, entity: entity}
	}
	if storage.InterventionJob != nil {
		storage.InterventionJob = &entityJob{Job: storage.InterventionJob, entity: entity}
	}
	for _, colorJobs := range storage.CodeJobs {
		for i, job := range colorJobs {
			if job != nil {
				colorJobs[i] = &entityJob{Job: job, entity: entity}
			}
		}
	}
	return storage
}

func initialCodeStatus(config *components.CodeConfig, previous *components.CodeStatus, now time.Time) *components.CodeStatus {
	status := &components.CodeStatus{Status: make(map[string]*components.ColorCodeStatus, len(config.Configs))}
	for color := range config.Configs {
		entry := &components.ColorCodeStatus{LastAlertTime: now}
		if previous != nil && previous.Status[color] != nil {
			*entry = *previous.Status[color]
		}
		status.Status[color] = entry
	}
	return status
}
