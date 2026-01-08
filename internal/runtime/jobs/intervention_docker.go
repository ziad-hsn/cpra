//go:build !nodocker

package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/swarm"
)

// InterventionDockerJob restarts Docker containers as an automated intervention.
// It uses a pooled Docker client for efficiency.
//
// Safety features:
//   - Uses pooled Docker client (not created per-call)
//   - Creates scoped context per attempt with proper cleanup
//   - Uses pre-allocated payloads to reduce allocations
type InterventionDockerJob struct {
	BaseJob
	Container  string
	DockerHost string
}

// Execute restarts the Docker container with retries.
func (i *InterventionDockerJob) Execute(ctx context.Context) Result {
	payload := GetInterventionDockerPayload()

	if err := validateContainerRef(i.Container); err != nil {
		return Result{
			Ent:     i.Entity,
			Err:     fmt.Errorf("container validation failed: %w", err),
			Payload: payload,
		}
	}

	cli, err := GetDockerClient(i.DockerHost)
	if err != nil {
		return Result{
			Ent:     i.Entity,
			Err:     fmt.Errorf("%w: %w", ErrFailedToCreateDockerClient, err),
			Payload: payload,
		}
	}

	timeout := int(i.Timeout.Seconds())
	restartOptions := container.StopOptions{Timeout: &timeout}

	cb := GetDockerCircuitBreaker()
	err = cb.Execute(ctx, func() error {
		return RetryWithBackoff(ctx, i.Retries+1, 50*time.Millisecond, func() error {
			attemptCtx, cancel := context.WithTimeout(ctx, i.Timeout)
			defer cancel()
			return cli.ContainerRestart(attemptCtx, i.Container, restartOptions)
		})
	})

	if err != nil {
		if err == context.Canceled || err == context.DeadlineExceeded {
			return Result{Ent: i.Entity, Err: err, Payload: payload}
		}
		return Result{Ent: i.Entity, Err: ErrDockerActionFailed, Payload: payload}
	}
	return Result{Ent: i.Entity, Err: nil, Payload: payload}
}

func (i *InterventionDockerJob) Copy() Job { job := *i; return &job }

func (i *InterventionDockerJob) Reset() {
	i.BaseJob.Reset()
	i.Container = ""
	i.DockerHost = ""
}

// IsNil checks if the job is nil.
func (i *InterventionDockerJob) IsNil() bool {
	return i == nil
}

// =============================================================================
// InterventionDockerStopJob - Graceful container stop (SIGTERM)
// =============================================================================

// InterventionDockerStopJob stops a Docker container gracefully with SIGTERM.
type InterventionDockerStopJob struct {
	BaseJob
	Container  string
	DockerHost string
}

// Execute stops the Docker container with retries.
func (j *InterventionDockerStopJob) Execute(ctx context.Context) Result {
	payload := GetInterventionDockerPayload()

	cli, err := GetDockerClient(j.DockerHost)
	if err != nil {
		return Result{Ent: j.Entity, Err: fmt.Errorf("%w: %w", ErrFailedToCreateDockerClient, err), Payload: payload}
	}

	timeout := int(j.Timeout.Seconds())
	stopOptions := container.StopOptions{Timeout: &timeout}

	err = RetryWithBackoff(ctx, j.Retries+1, 50*time.Millisecond, func() error {
		attemptCtx, cancel := context.WithTimeout(ctx, j.Timeout)
		defer cancel()
		return cli.ContainerStop(attemptCtx, j.Container, stopOptions)
	})

	if err != nil {
		if err == context.Canceled || err == context.DeadlineExceeded {
			return Result{Ent: j.Entity, Err: err, Payload: payload}
		}
		return Result{Ent: j.Entity, Err: ErrDockerStopFailed, Payload: payload}
	}
	return Result{Ent: j.Entity, Err: nil, Payload: payload}
}

func (j *InterventionDockerStopJob) Copy() Job { job := *j; return &job }

func (j *InterventionDockerStopJob) Reset() {
	j.BaseJob.Reset()
	j.Container = ""
	j.DockerHost = ""
}

// =============================================================================
// InterventionDockerStartJob - Start a stopped container
// =============================================================================

// InterventionDockerStartJob starts a stopped Docker container.
type InterventionDockerStartJob struct {
	BaseJob
	Container  string
	DockerHost string
}

// Execute starts the Docker container with retries.
func (j *InterventionDockerStartJob) Execute(ctx context.Context) Result {
	payload := GetInterventionDockerPayload()

	cli, err := GetDockerClient(j.DockerHost)
	if err != nil {
		return Result{Ent: j.Entity, Err: fmt.Errorf("%w: %w", ErrFailedToCreateDockerClient, err), Payload: payload}
	}

	err = RetryWithBackoff(ctx, j.Retries+1, 50*time.Millisecond, func() error {
		attemptCtx, cancel := context.WithTimeout(ctx, j.Timeout)
		defer cancel()
		return cli.ContainerStart(attemptCtx, j.Container, container.StartOptions{})
	})

	if err != nil {
		if err == context.Canceled || err == context.DeadlineExceeded {
			return Result{Ent: j.Entity, Err: err, Payload: payload}
		}
		return Result{Ent: j.Entity, Err: ErrDockerStartFailed, Payload: payload}
	}
	return Result{Ent: j.Entity, Err: nil, Payload: payload}
}

func (j *InterventionDockerStartJob) Copy() Job { job := *j; return &job }

func (j *InterventionDockerStartJob) Reset() {
	j.BaseJob.Reset()
	j.Container = ""
	j.DockerHost = ""
}

// =============================================================================
// InterventionDockerKillJob - Force kill container (SIGKILL or custom signal)
// =============================================================================

// InterventionDockerKillJob forcefully kills a Docker container.
type InterventionDockerKillJob struct {
	BaseJob
	Container  string
	DockerHost string
	Signal     string // Default: SIGKILL
}

// Execute kills the Docker container with retries.
func (j *InterventionDockerKillJob) Execute(ctx context.Context) Result {
	payload := GetInterventionDockerPayload()

	cli, err := GetDockerClient(j.DockerHost)
	if err != nil {
		return Result{Ent: j.Entity, Err: fmt.Errorf("%w: %w", ErrFailedToCreateDockerClient, err), Payload: payload}
	}

	signal := j.Signal
	if signal == "" {
		signal = "SIGKILL"
	}

	err = RetryWithBackoff(ctx, j.Retries+1, 50*time.Millisecond, func() error {
		return cli.ContainerKill(ctx, j.Container, signal)
	})

	if err != nil {
		if err == context.Canceled || err == context.DeadlineExceeded {
			return Result{Ent: j.Entity, Err: err, Payload: payload}
		}
		return Result{Ent: j.Entity, Err: ErrDockerKillFailed, Payload: payload}
	}
	return Result{Ent: j.Entity, Err: nil, Payload: payload}
}

func (j *InterventionDockerKillJob) Copy() Job { job := *j; return &job }

func (j *InterventionDockerKillJob) Reset() {
	j.BaseJob.Reset()
	j.Container = ""
	j.DockerHost = ""
	j.Signal = ""
}

// =============================================================================
// InterventionDockerPauseJob - Freeze container processes
// =============================================================================

// InterventionDockerPauseJob pauses (freezes) a Docker container's processes.
type InterventionDockerPauseJob struct {
	BaseJob
	Container  string
	DockerHost string
}

// Execute pauses the Docker container with retries.
func (j *InterventionDockerPauseJob) Execute(ctx context.Context) Result {
	payload := GetInterventionDockerPayload()

	cli, err := GetDockerClient(j.DockerHost)
	if err != nil {
		return Result{Ent: j.Entity, Err: fmt.Errorf("%w: %w", ErrFailedToCreateDockerClient, err), Payload: payload}
	}

	err = RetryWithBackoff(ctx, j.Retries+1, 50*time.Millisecond, func() error {
		return cli.ContainerPause(ctx, j.Container)
	})

	if err != nil {
		if err == context.Canceled || err == context.DeadlineExceeded {
			return Result{Ent: j.Entity, Err: err, Payload: payload}
		}
		return Result{Ent: j.Entity, Err: ErrDockerPauseFailed, Payload: payload}
	}
	return Result{Ent: j.Entity, Err: nil, Payload: payload}
}

func (j *InterventionDockerPauseJob) Copy() Job { job := *j; return &job }

func (j *InterventionDockerPauseJob) Reset() {
	j.BaseJob.Reset()
	j.Container = ""
	j.DockerHost = ""
}

// =============================================================================
// InterventionDockerUnpauseJob - Resume frozen container processes
// =============================================================================

// InterventionDockerUnpauseJob unpauses (resumes) a Docker container's processes.
type InterventionDockerUnpauseJob struct {
	BaseJob
	Container  string
	DockerHost string
}

// Execute unpauses the Docker container with retries.
func (j *InterventionDockerUnpauseJob) Execute(ctx context.Context) Result {
	payload := GetInterventionDockerPayload()

	cli, err := GetDockerClient(j.DockerHost)
	if err != nil {
		return Result{Ent: j.Entity, Err: fmt.Errorf("%w: %w", ErrFailedToCreateDockerClient, err), Payload: payload}
	}

	err = RetryWithBackoff(ctx, j.Retries+1, 50*time.Millisecond, func() error {
		return cli.ContainerUnpause(ctx, j.Container)
	})

	if err != nil {
		if err == context.Canceled || err == context.DeadlineExceeded {
			return Result{Ent: j.Entity, Err: err, Payload: payload}
		}
		return Result{Ent: j.Entity, Err: ErrDockerUnpauseFailed, Payload: payload}
	}
	return Result{Ent: j.Entity, Err: nil, Payload: payload}
}

func (j *InterventionDockerUnpauseJob) Copy() Job { job := *j; return &job }

func (j *InterventionDockerUnpauseJob) Reset() {
	j.BaseJob.Reset()
	j.Container = ""
	j.DockerHost = ""
}

// =============================================================================
// InterventionDockerScaleJob - Scale Swarm service replicas
// =============================================================================

// InterventionDockerScaleJob scales a Docker Swarm service's replica count.
type InterventionDockerScaleJob struct {
	BaseJob
	Service    string
	DockerHost string
	Replicas   uint64
}

// Execute scales the Swarm service with retries.
func (j *InterventionDockerScaleJob) Execute(ctx context.Context) Result {
	payload := GetInterventionDockerPayload()

	cli, err := GetDockerClient(j.DockerHost)
	if err != nil {
		return Result{Ent: j.Entity, Err: fmt.Errorf("%w: %w", ErrFailedToCreateDockerClient, err), Payload: payload}
	}

	var notReplicated bool
	err = RetryWithBackoff(ctx, j.Retries+1, 50*time.Millisecond, func() error {
		attemptCtx, cancel := context.WithTimeout(ctx, j.Timeout)
		defer cancel()

		// Inspect service to get current spec and version
		svc, _, inspectErr := cli.ServiceInspectWithRaw(attemptCtx, j.Service, swarm.ServiceInspectOptions{})
		if inspectErr != nil {
			return inspectErr
		}

		// Verify service is in replicated mode (non-retryable)
		if svc.Spec.Mode.Replicated == nil {
			notReplicated = true
			return ErrNotReplicatedService
		}

		// Update replicas
		replicas := j.Replicas
		svc.Spec.Mode.Replicated.Replicas = &replicas

		// Apply update
		_, updateErr := cli.ServiceUpdate(attemptCtx, svc.ID, svc.Version, svc.Spec, swarm.ServiceUpdateOptions{})
		return updateErr
	})

	if err != nil {
		if notReplicated {
			return Result{Ent: j.Entity, Err: ErrNotReplicatedService, Payload: payload}
		}
		if err == context.Canceled || err == context.DeadlineExceeded {
			return Result{Ent: j.Entity, Err: err, Payload: payload}
		}
		return Result{Ent: j.Entity, Err: ErrDockerScaleFailed, Payload: payload}
	}
	return Result{Ent: j.Entity, Err: nil, Payload: payload}
}

func (j *InterventionDockerScaleJob) Copy() Job { job := *j; return &job }

func (j *InterventionDockerScaleJob) Reset() {
	j.BaseJob.Reset()
	j.Service = ""
	j.DockerHost = ""
	j.Replicas = 0
}
