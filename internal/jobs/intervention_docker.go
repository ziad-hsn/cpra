package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/mlange-42/ark/ecs"
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
	EnqueueTime time.Time
	StartTime   time.Time
	Container   string
	DockerHost  string
	JobType     string
	Driver      string
	Timeout     time.Duration
	Retries     int
	Entity      ecs.Entity
}

// Execute restarts the Docker container with retries.
func (i *InterventionDockerJob) Execute(ctx context.Context) Result {
	// Use pre-allocated payload to reduce allocations
	payload := GetInterventionDockerPayload()

	// Use a pooled client instead of creating a new one
	cli, err := GetDockerClient(i.DockerHost)
	if err != nil {
		return Result{
			Ent:     i.Entity,
			Err:     fmt.Errorf("%w: %w", ErrFailedToCreateDockerClient, err),
			Payload: payload,
		}
	}
	// Do not close pooled client

	attempts := i.Retries + 1
	for attempt := 0; attempt < attempts; attempt++ {
		// Check context before each attempt
		select {
		case <-ctx.Done():
			return Result{Ent: i.Entity, Err: ctx.Err(), Payload: payload}
		default:
		}

		// Create scoped context with timeout for this attempt
		attemptCtx, cancel := context.WithTimeout(ctx, i.Timeout)
		timeout := int(i.Timeout.Seconds())
		restartOptions := container.StopOptions{Timeout: &timeout}
		err := cli.ContainerRestart(attemptCtx, i.Container, restartOptions)
		cancel() // Clean up context immediately after use

		if err == nil {
			return Result{Ent: i.Entity, Err: nil, Payload: payload}
		}

		// Brief pause before retry (don't block on last attempt)
		if attempt < attempts-1 {
			time.Sleep(50 * time.Millisecond)
		}
	}
	return Result{Ent: i.Entity, Err: ErrDockerActionFailed, Payload: payload}
}

// Copy returns a shallow copy of the job for safe pool reuse.
func (i *InterventionDockerJob) Copy() Job { job := *i; return &job }

// GetEnqueueTime returns when the job was enqueued.
func (i *InterventionDockerJob) GetEnqueueTime() time.Time { return i.EnqueueTime }

// SetEnqueueTime sets when the job was enqueued.
func (i *InterventionDockerJob) SetEnqueueTime(t time.Time) { i.EnqueueTime = t }

// GetStartTime returns when the job started executing.
func (i *InterventionDockerJob) GetStartTime() time.Time { return i.StartTime }

// SetStartTime sets when the job started executing.
func (i *InterventionDockerJob) SetStartTime(t time.Time) { i.StartTime = t }

// IsNil returns true if the job pointer is nil.
func (i *InterventionDockerJob) IsNil() bool { return i == nil }

// =============================================================================
// InterventionDockerStopJob - Graceful container stop (SIGTERM)
// =============================================================================

// InterventionDockerStopJob stops a Docker container gracefully with SIGTERM.
type InterventionDockerStopJob struct {
	EnqueueTime time.Time
	StartTime   time.Time
	Container   string
	DockerHost  string
	Timeout     time.Duration
	Retries     int
	Entity      ecs.Entity
}

// Execute stops the Docker container with retries.
func (j *InterventionDockerStopJob) Execute(ctx context.Context) Result {
	payload := GetInterventionDockerPayload()

	cli, err := GetDockerClient(j.DockerHost)
	if err != nil {
		return Result{Ent: j.Entity, Err: fmt.Errorf("%w: %w", ErrFailedToCreateDockerClient, err), Payload: payload}
	}

	attempts := j.Retries + 1
	for attempt := 0; attempt < attempts; attempt++ {
		select {
		case <-ctx.Done():
			return Result{Ent: j.Entity, Err: ctx.Err(), Payload: payload}
		default:
		}

		attemptCtx, cancel := context.WithTimeout(ctx, j.Timeout)
		timeout := int(j.Timeout.Seconds())
		err := cli.ContainerStop(attemptCtx, j.Container, container.StopOptions{Timeout: &timeout})
		cancel()

		if err == nil {
			return Result{Ent: j.Entity, Err: nil, Payload: payload}
		}

		if attempt < attempts-1 {
			time.Sleep(50 * time.Millisecond)
		}
	}
	return Result{Ent: j.Entity, Err: ErrDockerStopFailed, Payload: payload}
}

func (j *InterventionDockerStopJob) Copy() Job                  { job := *j; return &job }
func (j *InterventionDockerStopJob) GetEnqueueTime() time.Time  { return j.EnqueueTime }
func (j *InterventionDockerStopJob) SetEnqueueTime(t time.Time) { j.EnqueueTime = t }
func (j *InterventionDockerStopJob) GetStartTime() time.Time    { return j.StartTime }
func (j *InterventionDockerStopJob) SetStartTime(t time.Time)   { j.StartTime = t }
func (j *InterventionDockerStopJob) IsNil() bool                { return j == nil }

// =============================================================================
// InterventionDockerStartJob - Start a stopped container
// =============================================================================

// InterventionDockerStartJob starts a stopped Docker container.
type InterventionDockerStartJob struct {
	EnqueueTime time.Time
	StartTime   time.Time
	Container   string
	DockerHost  string
	Timeout     time.Duration
	Retries     int
	Entity      ecs.Entity
}

// Execute starts the Docker container with retries.
func (j *InterventionDockerStartJob) Execute(ctx context.Context) Result {
	payload := GetInterventionDockerPayload()

	cli, err := GetDockerClient(j.DockerHost)
	if err != nil {
		return Result{Ent: j.Entity, Err: fmt.Errorf("%w: %w", ErrFailedToCreateDockerClient, err), Payload: payload}
	}

	attempts := j.Retries + 1
	for attempt := 0; attempt < attempts; attempt++ {
		select {
		case <-ctx.Done():
			return Result{Ent: j.Entity, Err: ctx.Err(), Payload: payload}
		default:
		}

		attemptCtx, cancel := context.WithTimeout(ctx, j.Timeout)
		err := cli.ContainerStart(attemptCtx, j.Container, container.StartOptions{})
		cancel()

		if err == nil {
			return Result{Ent: j.Entity, Err: nil, Payload: payload}
		}

		if attempt < attempts-1 {
			time.Sleep(50 * time.Millisecond)
		}
	}
	return Result{Ent: j.Entity, Err: ErrDockerStartFailed, Payload: payload}
}

func (j *InterventionDockerStartJob) Copy() Job                  { job := *j; return &job }
func (j *InterventionDockerStartJob) GetEnqueueTime() time.Time  { return j.EnqueueTime }
func (j *InterventionDockerStartJob) SetEnqueueTime(t time.Time) { j.EnqueueTime = t }
func (j *InterventionDockerStartJob) GetStartTime() time.Time    { return j.StartTime }
func (j *InterventionDockerStartJob) SetStartTime(t time.Time)   { j.StartTime = t }
func (j *InterventionDockerStartJob) IsNil() bool                { return j == nil }

// =============================================================================
// InterventionDockerKillJob - Force kill container (SIGKILL or custom signal)
// =============================================================================

// InterventionDockerKillJob forcefully kills a Docker container.
type InterventionDockerKillJob struct {
	EnqueueTime time.Time
	StartTime   time.Time
	Container   string
	DockerHost  string
	Signal      string // Default: SIGKILL
	Retries     int
	Entity      ecs.Entity
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

	attempts := j.Retries + 1
	for attempt := 0; attempt < attempts; attempt++ {
		select {
		case <-ctx.Done():
			return Result{Ent: j.Entity, Err: ctx.Err(), Payload: payload}
		default:
		}

		err := cli.ContainerKill(ctx, j.Container, signal)
		if err == nil {
			return Result{Ent: j.Entity, Err: nil, Payload: payload}
		}

		if attempt < attempts-1 {
			time.Sleep(50 * time.Millisecond)
		}
	}
	return Result{Ent: j.Entity, Err: ErrDockerKillFailed, Payload: payload}
}

func (j *InterventionDockerKillJob) Copy() Job                  { job := *j; return &job }
func (j *InterventionDockerKillJob) GetEnqueueTime() time.Time  { return j.EnqueueTime }
func (j *InterventionDockerKillJob) SetEnqueueTime(t time.Time) { j.EnqueueTime = t }
func (j *InterventionDockerKillJob) GetStartTime() time.Time    { return j.StartTime }
func (j *InterventionDockerKillJob) SetStartTime(t time.Time)   { j.StartTime = t }
func (j *InterventionDockerKillJob) IsNil() bool                { return j == nil }

// =============================================================================
// InterventionDockerPauseJob - Freeze container processes
// =============================================================================

// InterventionDockerPauseJob pauses (freezes) a Docker container's processes.
type InterventionDockerPauseJob struct {
	EnqueueTime time.Time
	StartTime   time.Time
	Container   string
	DockerHost  string
	Retries     int
	Entity      ecs.Entity
}

// Execute pauses the Docker container with retries.
func (j *InterventionDockerPauseJob) Execute(ctx context.Context) Result {
	payload := GetInterventionDockerPayload()

	cli, err := GetDockerClient(j.DockerHost)
	if err != nil {
		return Result{Ent: j.Entity, Err: fmt.Errorf("%w: %w", ErrFailedToCreateDockerClient, err), Payload: payload}
	}

	attempts := j.Retries + 1
	for attempt := 0; attempt < attempts; attempt++ {
		select {
		case <-ctx.Done():
			return Result{Ent: j.Entity, Err: ctx.Err(), Payload: payload}
		default:
		}

		err := cli.ContainerPause(ctx, j.Container)
		if err == nil {
			return Result{Ent: j.Entity, Err: nil, Payload: payload}
		}

		if attempt < attempts-1 {
			time.Sleep(50 * time.Millisecond)
		}
	}
	return Result{Ent: j.Entity, Err: ErrDockerPauseFailed, Payload: payload}
}

func (j *InterventionDockerPauseJob) Copy() Job                  { job := *j; return &job }
func (j *InterventionDockerPauseJob) GetEnqueueTime() time.Time  { return j.EnqueueTime }
func (j *InterventionDockerPauseJob) SetEnqueueTime(t time.Time) { j.EnqueueTime = t }
func (j *InterventionDockerPauseJob) GetStartTime() time.Time    { return j.StartTime }
func (j *InterventionDockerPauseJob) SetStartTime(t time.Time)   { j.StartTime = t }
func (j *InterventionDockerPauseJob) IsNil() bool                { return j == nil }

// =============================================================================
// InterventionDockerUnpauseJob - Resume frozen container processes
// =============================================================================

// InterventionDockerUnpauseJob unpauses (resumes) a Docker container's processes.
type InterventionDockerUnpauseJob struct {
	EnqueueTime time.Time
	StartTime   time.Time
	Container   string
	DockerHost  string
	Retries     int
	Entity      ecs.Entity
}

// Execute unpauses the Docker container with retries.
func (j *InterventionDockerUnpauseJob) Execute(ctx context.Context) Result {
	payload := GetInterventionDockerPayload()

	cli, err := GetDockerClient(j.DockerHost)
	if err != nil {
		return Result{Ent: j.Entity, Err: fmt.Errorf("%w: %w", ErrFailedToCreateDockerClient, err), Payload: payload}
	}

	attempts := j.Retries + 1
	for attempt := 0; attempt < attempts; attempt++ {
		select {
		case <-ctx.Done():
			return Result{Ent: j.Entity, Err: ctx.Err(), Payload: payload}
		default:
		}

		err := cli.ContainerUnpause(ctx, j.Container)
		if err == nil {
			return Result{Ent: j.Entity, Err: nil, Payload: payload}
		}

		if attempt < attempts-1 {
			time.Sleep(50 * time.Millisecond)
		}
	}
	return Result{Ent: j.Entity, Err: ErrDockerUnpauseFailed, Payload: payload}
}

func (j *InterventionDockerUnpauseJob) Copy() Job                  { job := *j; return &job }
func (j *InterventionDockerUnpauseJob) GetEnqueueTime() time.Time  { return j.EnqueueTime }
func (j *InterventionDockerUnpauseJob) SetEnqueueTime(t time.Time) { j.EnqueueTime = t }
func (j *InterventionDockerUnpauseJob) GetStartTime() time.Time    { return j.StartTime }
func (j *InterventionDockerUnpauseJob) SetStartTime(t time.Time)   { j.StartTime = t }
func (j *InterventionDockerUnpauseJob) IsNil() bool                { return j == nil }

// =============================================================================
// InterventionDockerScaleJob - Scale Swarm service replicas
// =============================================================================

// InterventionDockerScaleJob scales a Docker Swarm service's replica count.
type InterventionDockerScaleJob struct {
	EnqueueTime time.Time
	StartTime   time.Time
	Service     string
	DockerHost  string
	Replicas    uint64
	Timeout     time.Duration
	Retries     int
	Entity      ecs.Entity
}

// Execute scales the Swarm service with retries.
func (j *InterventionDockerScaleJob) Execute(ctx context.Context) Result {
	payload := GetInterventionDockerPayload()

	cli, err := GetDockerClient(j.DockerHost)
	if err != nil {
		return Result{Ent: j.Entity, Err: fmt.Errorf("%w: %w", ErrFailedToCreateDockerClient, err), Payload: payload}
	}

	attempts := j.Retries + 1
	for attempt := 0; attempt < attempts; attempt++ {
		select {
		case <-ctx.Done():
			return Result{Ent: j.Entity, Err: ctx.Err(), Payload: payload}
		default:
		}

		attemptCtx, cancel := context.WithTimeout(ctx, j.Timeout)

		// Inspect service to get current spec and version
		svc, _, err := cli.ServiceInspectWithRaw(attemptCtx, j.Service, swarm.ServiceInspectOptions{})
		if err != nil {
			cancel()
			if attempt < attempts-1 {
				time.Sleep(50 * time.Millisecond)
			}
			continue
		}

		// Verify service is in replicated mode
		if svc.Spec.Mode.Replicated == nil {
			cancel()
			return Result{Ent: j.Entity, Err: ErrNotReplicatedService, Payload: payload}
		}

		// Update replicas
		replicas := j.Replicas
		svc.Spec.Mode.Replicated.Replicas = &replicas

		// Apply update
		_, err = cli.ServiceUpdate(attemptCtx, svc.ID, svc.Version, svc.Spec, swarm.ServiceUpdateOptions{})
		cancel()

		if err == nil {
			return Result{Ent: j.Entity, Err: nil, Payload: payload}
		}

		if attempt < attempts-1 {
			time.Sleep(50 * time.Millisecond)
		}
	}
	return Result{Ent: j.Entity, Err: ErrDockerScaleFailed, Payload: payload}
}

func (j *InterventionDockerScaleJob) Copy() Job                  { job := *j; return &job }
func (j *InterventionDockerScaleJob) GetEnqueueTime() time.Time  { return j.EnqueueTime }
func (j *InterventionDockerScaleJob) SetEnqueueTime(t time.Time) { j.EnqueueTime = t }
func (j *InterventionDockerScaleJob) GetStartTime() time.Time    { return j.StartTime }
func (j *InterventionDockerScaleJob) SetStartTime(t time.Time)   { j.StartTime = t }
func (j *InterventionDockerScaleJob) IsNil() bool                { return j == nil }
