//go:build !nodocker

package jobs

import (
	"sync"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// Top-level factory functions for sync.Pool - eliminates closure allocations at init
func newInterventionDockerJob() any        { return &InterventionDockerJob{} }
func newInterventionDockerStopJob() any    { return &InterventionDockerStopJob{} }
func newInterventionDockerStartJob() any   { return &InterventionDockerStartJob{} }
func newInterventionDockerKillJob() any    { return &InterventionDockerKillJob{} }
func newInterventionDockerPauseJob() any   { return &InterventionDockerPauseJob{} }
func newInterventionDockerUnpauseJob() any { return &InterventionDockerUnpauseJob{} }
func newInterventionDockerScaleJob() any   { return &InterventionDockerScaleJob{} }

var (
	interventionDockerJobPool        = sync.Pool{New: newInterventionDockerJob}
	interventionDockerStopJobPool    = sync.Pool{New: newInterventionDockerStopJob}
	interventionDockerStartJobPool   = sync.Pool{New: newInterventionDockerStartJob}
	interventionDockerKillJobPool    = sync.Pool{New: newInterventionDockerKillJob}
	interventionDockerPauseJobPool   = sync.Pool{New: newInterventionDockerPauseJob}
	interventionDockerUnpauseJobPool = sync.Pool{New: newInterventionDockerUnpauseJob}
	interventionDockerScaleJobPool   = sync.Pool{New: newInterventionDockerScaleJob}
)

func getInterventionDockerJob() *InterventionDockerJob {
	return interventionDockerJobPool.Get().(*InterventionDockerJob)
}
func getInterventionDockerStopJob() *InterventionDockerStopJob {
	return interventionDockerStopJobPool.Get().(*InterventionDockerStopJob)
}
func getInterventionDockerStartJob() *InterventionDockerStartJob {
	return interventionDockerStartJobPool.Get().(*InterventionDockerStartJob)
}
func getInterventionDockerKillJob() *InterventionDockerKillJob {
	return interventionDockerKillJobPool.Get().(*InterventionDockerKillJob)
}
func getInterventionDockerPauseJob() *InterventionDockerPauseJob {
	return interventionDockerPauseJobPool.Get().(*InterventionDockerPauseJob)
}
func getInterventionDockerUnpauseJob() *InterventionDockerUnpauseJob {
	return interventionDockerUnpauseJobPool.Get().(*InterventionDockerUnpauseJob)
}
func getInterventionDockerScaleJob() *InterventionDockerScaleJob {
	return interventionDockerScaleJobPool.Get().(*InterventionDockerScaleJob)
}

func releaseInterventionJob(job Job) {
	switch j := job.(type) {
	case *InterventionDockerJob:
		resetInterventionDockerJob(j)
		interventionDockerJobPool.Put(j)
	case *InterventionDockerStopJob:
		resetInterventionDockerStopJob(j)
		interventionDockerStopJobPool.Put(j)
	case *InterventionDockerStartJob:
		resetInterventionDockerStartJob(j)
		interventionDockerStartJobPool.Put(j)
	case *InterventionDockerKillJob:
		resetInterventionDockerKillJob(j)
		interventionDockerKillJobPool.Put(j)
	case *InterventionDockerPauseJob:
		resetInterventionDockerPauseJob(j)
		interventionDockerPauseJobPool.Put(j)
	case *InterventionDockerUnpauseJob:
		resetInterventionDockerUnpauseJob(j)
		interventionDockerUnpauseJobPool.Put(j)
	case *InterventionDockerScaleJob:
		resetInterventionDockerScaleJob(j)
		interventionDockerScaleJobPool.Put(j)
	}
}

func resetInterventionDockerJob(job *InterventionDockerJob) {
	if job == nil {
		return
	}
	job.EnqueueTime = time.Time{}
	job.StartTime = time.Time{}
	job.Container = ""
	job.DockerHost = ""
	job.Timeout = 0
	job.Retries = 0
	job.Entity = ecs.Entity{}
	// JobType and Driver are set on creation, don't clear
}

func resetInterventionDockerStopJob(job *InterventionDockerStopJob) {
	if job == nil {
		return
	}
	job.EnqueueTime = time.Time{}
	job.StartTime = time.Time{}
	job.Container = ""
	job.DockerHost = ""
	job.Timeout = 0
	job.Retries = 0
	job.Entity = ecs.Entity{}
}

func resetInterventionDockerStartJob(job *InterventionDockerStartJob) {
	if job == nil {
		return
	}
	job.EnqueueTime = time.Time{}
	job.StartTime = time.Time{}
	job.Container = ""
	job.DockerHost = ""
	job.Timeout = 0
	job.Retries = 0
	job.Entity = ecs.Entity{}
}

func resetInterventionDockerKillJob(job *InterventionDockerKillJob) {
	if job == nil {
		return
	}
	job.EnqueueTime = time.Time{}
	job.StartTime = time.Time{}
	job.Container = ""
	job.DockerHost = ""
	job.Signal = ""
	job.Retries = 0
	job.Entity = ecs.Entity{}
}

func resetInterventionDockerPauseJob(job *InterventionDockerPauseJob) {
	if job == nil {
		return
	}
	job.EnqueueTime = time.Time{}
	job.StartTime = time.Time{}
	job.Container = ""
	job.DockerHost = ""
	job.Retries = 0
	job.Entity = ecs.Entity{}
}

func resetInterventionDockerUnpauseJob(job *InterventionDockerUnpauseJob) {
	if job == nil {
		return
	}
	job.EnqueueTime = time.Time{}
	job.StartTime = time.Time{}
	job.Container = ""
	job.DockerHost = ""
	job.Retries = 0
	job.Entity = ecs.Entity{}
}

func resetInterventionDockerScaleJob(job *InterventionDockerScaleJob) {
	if job == nil {
		return
	}
	job.EnqueueTime = time.Time{}
	job.StartTime = time.Time{}
	job.Service = ""
	job.DockerHost = ""
	job.Replicas = 0
	job.Timeout = 0
	job.Retries = 0
	job.Entity = ecs.Entity{}
}

