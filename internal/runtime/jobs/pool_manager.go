package jobs

import (
	"sync"
)

// JobPoolManager manages sync.Pool instances for all job types.
// It encapsulates pool lifecycle and provides a clean interface for acquiring/releasing jobs.
//
// This replaces the global pool variables, enabling:
//   - Dependency injection in tests
//   - Better separation of concerns
//   - Consistent job lifecycle management
type JobPoolManager struct {
	pools map[string]*sync.Pool
}

// NewJobPoolManager creates a new JobPoolManager with all job pools initialized.
func NewJobPoolManager() *JobPoolManager {
	return &JobPoolManager{
		pools: map[string]*sync.Pool{
			// Pulse jobs
			"pulse_http": {New: newPulseHTTPJob},
			"pulse_tcp":  {New: newPulseTCPJob},
			"pulse_icmp": {New: newPulseICMPJob},

			// Intervention jobs
			"intervention_docker":         {New: newInterventionDockerJob},
			"intervention_docker_stop":    {New: newInterventionDockerStopJob},
			"intervention_docker_start":   {New: newInterventionDockerStartJob},
			"intervention_docker_kill":    {New: newInterventionDockerKillJob},
			"intervention_docker_pause":   {New: newInterventionDockerPauseJob},
			"intervention_docker_unpause": {New: newInterventionDockerUnpauseJob},
			"intervention_docker_scale":   {New: newInterventionDockerScaleJob},

			// Code jobs
			"code_log":          {New: newCodeLogJob},
			"code_notification": {New: newCodeNotificationJob},
		},
	}
}

// GetPulseHTTPJob acquires a PulseHTTPJob from the pool.
func (m *JobPoolManager) GetPulseHTTPJob() *PulseHTTPJob {
	return m.pools["pulse_http"].Get().(*PulseHTTPJob)
}

// GetPulseTCPJob acquires a PulseTCPJob from the pool.
func (m *JobPoolManager) GetPulseTCPJob() *PulseTCPJob {
	return m.pools["pulse_tcp"].Get().(*PulseTCPJob)
}

// GetPulseICMPJob acquires a PulseICMPJob from the pool.
func (m *JobPoolManager) GetPulseICMPJob() *PulseICMPJob {
	return m.pools["pulse_icmp"].Get().(*PulseICMPJob)
}

// GetInterventionDockerJob acquires an InterventionDockerJob from the pool.
func (m *JobPoolManager) GetInterventionDockerJob() *InterventionDockerJob {
	return m.pools["intervention_docker"].Get().(*InterventionDockerJob)
}

// GetInterventionDockerStopJob acquires an InterventionDockerStopJob from the pool.
func (m *JobPoolManager) GetInterventionDockerStopJob() *InterventionDockerStopJob {
	return m.pools["intervention_docker_stop"].Get().(*InterventionDockerStopJob)
}

// GetInterventionDockerStartJob acquires an InterventionDockerStartJob from the pool.
func (m *JobPoolManager) GetInterventionDockerStartJob() *InterventionDockerStartJob {
	return m.pools["intervention_docker_start"].Get().(*InterventionDockerStartJob)
}

// GetInterventionDockerKillJob acquires an InterventionDockerKillJob from the pool.
func (m *JobPoolManager) GetInterventionDockerKillJob() *InterventionDockerKillJob {
	return m.pools["intervention_docker_kill"].Get().(*InterventionDockerKillJob)
}

// GetInterventionDockerPauseJob acquires an InterventionDockerPauseJob from the pool.
func (m *JobPoolManager) GetInterventionDockerPauseJob() *InterventionDockerPauseJob {
	return m.pools["intervention_docker_pause"].Get().(*InterventionDockerPauseJob)
}

// GetInterventionDockerUnpauseJob acquires an InterventionDockerUnpauseJob from the pool.
func (m *JobPoolManager) GetInterventionDockerUnpauseJob() *InterventionDockerUnpauseJob {
	return m.pools["intervention_docker_unpause"].Get().(*InterventionDockerUnpauseJob)
}

// GetInterventionDockerScaleJob acquires an InterventionDockerScaleJob from the pool.
func (m *JobPoolManager) GetInterventionDockerScaleJob() *InterventionDockerScaleJob {
	return m.pools["intervention_docker_scale"].Get().(*InterventionDockerScaleJob)
}

// GetCodeLogJob acquires a CodeLogJob from the pool.
func (m *JobPoolManager) GetCodeLogJob() *CodeLogJob {
	return m.pools["code_log"].Get().(*CodeLogJob)
}

// GetCodeNotificationJob acquires a CodeNotificationJob from the pool.
func (m *JobPoolManager) GetCodeNotificationJob() *CodeNotificationJob {
	return m.pools["code_notification"].Get().(*CodeNotificationJob)
}

// ReleasePulseJob returns a pulse job back to its pool.
func (m *JobPoolManager) ReleasePulseJob(job Job) {
	switch j := job.(type) {
	case *PulseHTTPJob:
		j.Reset()
		m.pools["pulse_http"].Put(j)
	case *PulseTCPJob:
		j.Reset()
		m.pools["pulse_tcp"].Put(j)
	case *PulseICMPJob:
		j.Reset()
		m.pools["pulse_icmp"].Put(j)
	}
}

// ReleaseInterventionJob returns an intervention job back to its pool.
func (m *JobPoolManager) ReleaseInterventionJob(job Job) {
	switch j := job.(type) {
	case *InterventionDockerJob:
		j.Reset()
		m.pools["intervention_docker"].Put(j)
	case *InterventionDockerStopJob:
		j.Reset()
		m.pools["intervention_docker_stop"].Put(j)
	case *InterventionDockerStartJob:
		j.Reset()
		m.pools["intervention_docker_start"].Put(j)
	case *InterventionDockerKillJob:
		j.Reset()
		m.pools["intervention_docker_kill"].Put(j)
	case *InterventionDockerPauseJob:
		j.Reset()
		m.pools["intervention_docker_pause"].Put(j)
	case *InterventionDockerUnpauseJob:
		j.Reset()
		m.pools["intervention_docker_unpause"].Put(j)
	case *InterventionDockerScaleJob:
		j.Reset()
		m.pools["intervention_docker_scale"].Put(j)
	}
}

// ReleaseCodeJob returns a code job back to its pool.
func (m *JobPoolManager) ReleaseCodeJob(job Job) {
	switch j := job.(type) {
	case *CodeLogJob:
		j.Reset()
		m.pools["code_log"].Put(j)
	case *CodeNotificationJob:
		j.Reset()
		m.pools["code_notification"].Put(j)
	}
}

// defaultPoolManager is the global default pool manager.
// This provides backward compatibility while allowing injection in tests.
var defaultPoolManager = NewJobPoolManager()

// DefaultPoolManager returns the global default pool manager.
// Prefer injecting a JobPoolManager instance instead of using this global.
func DefaultPoolManager() *JobPoolManager {
	return defaultPoolManager
}
