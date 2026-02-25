package entities

import (
	"sync"

	"cpra/internal/controller/components"
	"cpra/internal/runtime/jobs"
)

var (
	monitorStatePool       = sync.Pool{New: func() any { return &components.MonitorState{} }}
	pulseConfigPool        = sync.Pool{New: func() any { return &components.PulseConfig{} }}
	interventionConfigPool = sync.Pool{New: func() any { return &components.InterventionConfig{} }}
	jobStoragePool         = sync.Pool{New: func() any {
		return &components.JobStorage{}
	}}
)

// GetMonitorState returns a pooled MonitorState.
func GetMonitorState() *components.MonitorState {
	return monitorStatePool.Get().(*components.MonitorState)
}

// PutMonitorState resets and pools a MonitorState.
func PutMonitorState(m *components.MonitorState) {
	if m == nil {
		return
	}
	*m = components.MonitorState{}
	monitorStatePool.Put(m)
}

// GetPulseConfig returns a pooled PulseConfig.
func GetPulseConfig() *components.PulseConfig {
	return pulseConfigPool.Get().(*components.PulseConfig)
}

// PutPulseConfig resets and pools a PulseConfig.
func PutPulseConfig(p *components.PulseConfig) {
	if p == nil {
		return
	}
	*p = components.PulseConfig{}
	pulseConfigPool.Put(p)
}

// GetInterventionConfig returns a pooled InterventionConfig.
func GetInterventionConfig() *components.InterventionConfig {
	return interventionConfigPool.Get().(*components.InterventionConfig)
}

// PutInterventionConfig resets and pools an InterventionConfig.
func PutInterventionConfig(i *components.InterventionConfig) {
	if i == nil {
		return
	}
	*i = components.InterventionConfig{}
	interventionConfigPool.Put(i)
}

// Pools for code components - now safe because arrays are value types, not reference types
var (
	codeConfigPool = sync.Pool{New: func() any { return &components.CodeConfig{} }}
	codeStatusPool = sync.Pool{New: func() any { return &components.CodeStatus{} }}
)

// GetCodeConfig returns a pooled CodeConfig.
// Safe to pool now because [MaxColors]ConfigID is a value type (copied by ECS).
func GetCodeConfig(_ int) *components.CodeConfig {
	cfg := codeConfigPool.Get().(*components.CodeConfig)
	*cfg = components.CodeConfig{} // Zero the array values
	return cfg
}

// PutCodeConfig returns a CodeConfig to the pool.
func PutCodeConfig(c *components.CodeConfig) {
	if c == nil {
		return
	}
	codeConfigPool.Put(c)
}

// GetCodeStatus returns a pooled CodeStatus.
// Safe to pool now because [MaxColors]ColorCodeStatus is a value type (copied by ECS).
func GetCodeStatus(_ int) *components.CodeStatus {
	status := codeStatusPool.Get().(*components.CodeStatus)
	*status = components.CodeStatus{} // Zero the array values
	return status
}

// PutCodeStatus returns a CodeStatus to the pool.
func PutCodeStatus(c *components.CodeStatus) {
	if c == nil {
		return
	}
	codeStatusPool.Put(c)
}

// GetColorCodeStatus returns a new ColorCodeStatus.
func GetColorCodeStatus() *components.ColorCodeStatus {
	return &components.ColorCodeStatus{}
}

// PutColorCodeStatus is a no-op (ColorCodeStatus is stored inline in the array).
func PutColorCodeStatus(c *components.ColorCodeStatus) {
	// No-op: ColorCodeStatus is a value type stored inline in CodeStatus.Status array
}

// GetJobStorage returns a pooled JobStorage.
func GetJobStorage() *components.JobStorage {
	storage := jobStoragePool.Get().(*components.JobStorage)
	return storage
}

// PutJobStorage releases held jobs and pools the JobStorage.
func PutJobStorage(j *components.JobStorage) {
	if j == nil {
		return
	}
	if j.PulseJob != nil {
		jobs.ReleasePulseJob(j.PulseJob)
		j.PulseJob = nil
	}
	if j.InterventionJob != nil {
		jobs.ReleaseInterventionJob(j.InterventionJob)
		j.InterventionJob = nil
	}
	jobStoragePool.Put(j)
}
