package entities

import (
	"sync"

	"cpra/internal/controller/components"
	"cpra/internal/jobs"
)

var (
	monitorStatePool       = sync.Pool{New: func() any { return &components.MonitorState{} }}
	pulseConfigPool        = sync.Pool{New: func() any { return &components.PulseConfig{} }}
	interventionConfigPool = sync.Pool{New: func() any { return &components.InterventionConfig{} }}
	codeConfigPool         = sync.Pool{New: func() any { return &components.CodeConfig{} }}
	colorCodeConfigPool    = sync.Pool{New: func() any { return &components.ColorCodeConfig{} }}
	codeStatusPool         = sync.Pool{New: func() any { return &components.CodeStatus{} }}
	colorCodeStatusPool    = sync.Pool{New: func() any { return &components.ColorCodeStatus{} }}
	jobStoragePool         = sync.Pool{New: func() any {
		return &components.JobStorage{CodeJobs: make(map[string]jobs.Job)}
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

// GetCodeConfig returns a pooled CodeConfig with an initialized map.
func GetCodeConfig(codeCount int) *components.CodeConfig {
	cfg := codeConfigPool.Get().(*components.CodeConfig)
	if cfg.Configs == nil {
		cfg.Configs = make(map[string]*components.ColorCodeConfig, codeCount)
	} else {
		for k := range cfg.Configs {
			delete(cfg.Configs, k)
		}
	}
	return cfg
}

// PutCodeConfig clears nested structures and pools the CodeConfig.
func PutCodeConfig(c *components.CodeConfig) {
	if c == nil {
		return
	}
	for k, cfg := range c.Configs {
		if cfg != nil {
			PutColorCodeConfig(cfg)
		}
		delete(c.Configs, k)
	}
	codeConfigPool.Put(c)
}

// GetColorCodeConfig returns a pooled ColorCodeConfig.
func GetColorCodeConfig() *components.ColorCodeConfig {
	return colorCodeConfigPool.Get().(*components.ColorCodeConfig)
}

// PutColorCodeConfig resets and pools a ColorCodeConfig.
func PutColorCodeConfig(c *components.ColorCodeConfig) {
	if c == nil {
		return
	}
	*c = components.ColorCodeConfig{}
	colorCodeConfigPool.Put(c)
}

// GetCodeStatus returns a pooled CodeStatus with an initialized map.
func GetCodeStatus(codeCount int) *components.CodeStatus {
	status := codeStatusPool.Get().(*components.CodeStatus)
	if status.Status == nil {
		status.Status = make(map[string]*components.ColorCodeStatus, codeCount)
	} else {
		for k := range status.Status {
			delete(status.Status, k)
		}
	}
	return status
}

// PutCodeStatus clears nested status entries and pools the CodeStatus.
func PutCodeStatus(c *components.CodeStatus) {
	if c == nil {
		return
	}
	for k, status := range c.Status {
		if status != nil {
			PutColorCodeStatus(status)
		}
		delete(c.Status, k)
	}
	codeStatusPool.Put(c)
}

// GetColorCodeStatus returns a pooled ColorCodeStatus.
func GetColorCodeStatus() *components.ColorCodeStatus {
	return colorCodeStatusPool.Get().(*components.ColorCodeStatus)
}

// PutColorCodeStatus resets and pools a ColorCodeStatus.
func PutColorCodeStatus(c *components.ColorCodeStatus) {
	if c == nil {
		return
	}
	*c = components.ColorCodeStatus{}
	colorCodeStatusPool.Put(c)
}

// GetJobStorage returns a pooled JobStorage with an initialized map sized for codeCount.
func GetJobStorage(codeCount int) *components.JobStorage {
	storage := jobStoragePool.Get().(*components.JobStorage)
	if storage.CodeJobs == nil {
		storage.CodeJobs = make(map[string]jobs.Job, codeCount)
	} else {
		for color := range storage.CodeJobs {
			delete(storage.CodeJobs, color)
		}
	}
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
	for color, job := range j.CodeJobs {
		if job != nil {
			jobs.ReleaseCodeJob(job)
		}
		delete(j.CodeJobs, color)
	}
	jobStoragePool.Put(j)
}
