package jobs

// Top-level factory functions for sync.Pool - eliminates closure allocations at init
// These are used by JobPoolManager to create new job instances.
func newPulseHTTPJob() any                 { return &PulseHTTPJob{} }
func newPulseTCPJob() any                  { return &PulseTCPJob{} }
func newPulseICMPJob() any                 { return &PulseICMPJob{} }
func newInterventionDockerJob() any        { return &InterventionDockerJob{} }
func newInterventionDockerStopJob() any    { return &InterventionDockerStopJob{} }
func newInterventionDockerStartJob() any   { return &InterventionDockerStartJob{} }
func newInterventionDockerKillJob() any    { return &InterventionDockerKillJob{} }
func newInterventionDockerPauseJob() any   { return &InterventionDockerPauseJob{} }
func newInterventionDockerUnpauseJob() any { return &InterventionDockerUnpauseJob{} }
func newInterventionDockerScaleJob() any   { return &InterventionDockerScaleJob{} }
func newCodeLogJob() any                   { return &CodeLogJob{} }
func newCodeNotificationJob() any          { return &CodeNotificationJob{} }

// Package-level convenience functions that delegate to the default JobPoolManager.
// These provide backward compatibility while allowing DI in new code.
// For new code, prefer injecting a JobPoolManager instance directly.

func getPulseHTTPJob() *PulseHTTPJob { return defaultPoolManager.GetPulseHTTPJob() }
func getPulseTCPJob() *PulseTCPJob   { return defaultPoolManager.GetPulseTCPJob() }
func getPulseICMPJob() *PulseICMPJob { return defaultPoolManager.GetPulseICMPJob() }

func getInterventionDockerJob() *InterventionDockerJob {
	return defaultPoolManager.GetInterventionDockerJob()
}
func getInterventionDockerStopJob() *InterventionDockerStopJob {
	return defaultPoolManager.GetInterventionDockerStopJob()
}
func getInterventionDockerStartJob() *InterventionDockerStartJob {
	return defaultPoolManager.GetInterventionDockerStartJob()
}
func getInterventionDockerKillJob() *InterventionDockerKillJob {
	return defaultPoolManager.GetInterventionDockerKillJob()
}
func getInterventionDockerPauseJob() *InterventionDockerPauseJob {
	return defaultPoolManager.GetInterventionDockerPauseJob()
}
func getInterventionDockerUnpauseJob() *InterventionDockerUnpauseJob {
	return defaultPoolManager.GetInterventionDockerUnpauseJob()
}
func getInterventionDockerScaleJob() *InterventionDockerScaleJob {
	return defaultPoolManager.GetInterventionDockerScaleJob()
}

func getCodeLogJob() *CodeLogJob                   { return defaultPoolManager.GetCodeLogJob() }
func getCodeNotificationJob() *CodeNotificationJob { return defaultPoolManager.GetCodeNotificationJob() }

// ReleasePulseJob returns a pulse job back to its pool.
// This delegates to the default JobPoolManager for backward compatibility.
func ReleasePulseJob(job Job) {
	defaultPoolManager.ReleasePulseJob(job)
}

// ReleaseInterventionJob returns an intervention job back to its pool.
// This delegates to the default JobPoolManager for backward compatibility.
func ReleaseInterventionJob(job Job) {
	defaultPoolManager.ReleaseInterventionJob(job)
}

// ReleaseCodeJob returns a code job back to its pool.
// This delegates to the default JobPoolManager for backward compatibility.
func ReleaseCodeJob(job Job) {
	defaultPoolManager.ReleaseCodeJob(job)
}
