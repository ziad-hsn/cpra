//go:build !nodocker

package jobs

import (
	"fmt"

	"github.com/mlange-42/ark/ecs"

	"cpra/internal/loader/schema"
)

// CreateInterventionJob creates a new intervention job based on the provided schema.
// Supports Docker interventions: restart, stop, start, kill, pause, unpause, scale.
//
// Jobs are obtained from sync.Pool for memory efficiency.
func CreateInterventionJob(interventionSchema schema.Intervention, jobID ecs.Entity) (Job, error) {
	retries := interventionSchema.Retries
	switch interventionSchema.Action {
	case "docker":
		target, ok := interventionSchema.Target.(*schema.InterventionTargetDocker)
		if !ok || target == nil {
			return nil, ErrDockerMissingTarget
		}

		// Route by target.Type (default to "restart" for backwards compatibility)
		switch target.Type {
		case "restart", "":
			job := getInterventionDockerJob()
			job.Entity = jobID
			job.Container = target.Container
			job.DockerHost = target.DockerHost
			job.Retries = retries
			job.Timeout = target.Timeout
			job.JobType = InternedIntervention
			job.Driver = InternedDocker
			return job, nil

		case "stop":
			job := getInterventionDockerStopJob()
			job.Entity = jobID
			job.Container = target.Container
			job.DockerHost = target.DockerHost
			job.Retries = retries
			job.Timeout = target.Timeout
			return job, nil

		case "start":
			job := getInterventionDockerStartJob()
			job.Entity = jobID
			job.Container = target.Container
			job.DockerHost = target.DockerHost
			job.Retries = retries
			job.Timeout = target.Timeout
			return job, nil

		case "kill":
			job := getInterventionDockerKillJob()
			job.Entity = jobID
			job.Container = target.Container
			job.DockerHost = target.DockerHost
			job.Signal = target.Signal
			job.Retries = retries
			return job, nil

		case "pause":
			job := getInterventionDockerPauseJob()
			job.Entity = jobID
			job.Container = target.Container
			job.DockerHost = target.DockerHost
			job.Retries = retries
			return job, nil

		case "unpause":
			job := getInterventionDockerUnpauseJob()
			job.Entity = jobID
			job.Container = target.Container
			job.DockerHost = target.DockerHost
			job.Retries = retries
			return job, nil

		case "scale":
			job := getInterventionDockerScaleJob()
			job.Entity = jobID
			job.Service = target.Service
			job.DockerHost = target.DockerHost
			job.Replicas = target.Replicas
			job.Retries = retries
			job.Timeout = target.Timeout
			return job, nil

		default:
			return nil, ErrUnknownDockerAction
		}

	default:
		return nil, fmt.Errorf("%w: %s", ErrUnknownInterventionAction, interventionSchema.Action)
	}
}

