//go:build nodocker

package jobs

import (
	"context"
	"time"

	"github.com/mlange-42/ark/ecs"
)

// Stub implementations for Docker intervention jobs when Docker support is disabled.
// These types exist to satisfy type assertions but Execute always returns ErrDockerDisabled.

// InterventionDockerJob is a stub when Docker is disabled.
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

func (i *InterventionDockerJob) Execute(ctx context.Context) Result {
	return Result{Ent: i.Entity, Err: ErrDockerDisabled, Payload: GetInterventionDockerPayload()}
}
func (i *InterventionDockerJob) Copy() Job                  { job := *i; return &job }
func (i *InterventionDockerJob) GetEnqueueTime() time.Time  { return i.EnqueueTime }
func (i *InterventionDockerJob) SetEnqueueTime(t time.Time) { i.EnqueueTime = t }
func (i *InterventionDockerJob) GetStartTime() time.Time    { return i.StartTime }
func (i *InterventionDockerJob) SetStartTime(t time.Time)   { i.StartTime = t }
func (i *InterventionDockerJob) IsNil() bool                { return i == nil }

// InterventionDockerStopJob is a stub when Docker is disabled.
type InterventionDockerStopJob struct {
	EnqueueTime time.Time
	StartTime   time.Time
	Container   string
	DockerHost  string
	Timeout     time.Duration
	Retries     int
	Entity      ecs.Entity
}

func (j *InterventionDockerStopJob) Execute(ctx context.Context) Result {
	return Result{Ent: j.Entity, Err: ErrDockerDisabled, Payload: GetInterventionDockerPayload()}
}
func (j *InterventionDockerStopJob) Copy() Job                  { job := *j; return &job }
func (j *InterventionDockerStopJob) GetEnqueueTime() time.Time  { return j.EnqueueTime }
func (j *InterventionDockerStopJob) SetEnqueueTime(t time.Time) { j.EnqueueTime = t }
func (j *InterventionDockerStopJob) GetStartTime() time.Time    { return j.StartTime }
func (j *InterventionDockerStopJob) SetStartTime(t time.Time)   { j.StartTime = t }
func (j *InterventionDockerStopJob) IsNil() bool                { return j == nil }

// InterventionDockerStartJob is a stub when Docker is disabled.
type InterventionDockerStartJob struct {
	EnqueueTime time.Time
	StartTime   time.Time
	Container   string
	DockerHost  string
	Timeout     time.Duration
	Retries     int
	Entity      ecs.Entity
}

func (j *InterventionDockerStartJob) Execute(ctx context.Context) Result {
	return Result{Ent: j.Entity, Err: ErrDockerDisabled, Payload: GetInterventionDockerPayload()}
}
func (j *InterventionDockerStartJob) Copy() Job                  { job := *j; return &job }
func (j *InterventionDockerStartJob) GetEnqueueTime() time.Time  { return j.EnqueueTime }
func (j *InterventionDockerStartJob) SetEnqueueTime(t time.Time) { j.EnqueueTime = t }
func (j *InterventionDockerStartJob) GetStartTime() time.Time    { return j.StartTime }
func (j *InterventionDockerStartJob) SetStartTime(t time.Time)   { j.StartTime = t }
func (j *InterventionDockerStartJob) IsNil() bool                { return j == nil }

// InterventionDockerKillJob is a stub when Docker is disabled.
type InterventionDockerKillJob struct {
	EnqueueTime time.Time
	StartTime   time.Time
	Container   string
	DockerHost  string
	Signal      string
	Retries     int
	Entity      ecs.Entity
}

func (j *InterventionDockerKillJob) Execute(ctx context.Context) Result {
	return Result{Ent: j.Entity, Err: ErrDockerDisabled, Payload: GetInterventionDockerPayload()}
}
func (j *InterventionDockerKillJob) Copy() Job                  { job := *j; return &job }
func (j *InterventionDockerKillJob) GetEnqueueTime() time.Time  { return j.EnqueueTime }
func (j *InterventionDockerKillJob) SetEnqueueTime(t time.Time) { j.EnqueueTime = t }
func (j *InterventionDockerKillJob) GetStartTime() time.Time    { return j.StartTime }
func (j *InterventionDockerKillJob) SetStartTime(t time.Time)   { j.StartTime = t }
func (j *InterventionDockerKillJob) IsNil() bool                { return j == nil }

// InterventionDockerPauseJob is a stub when Docker is disabled.
type InterventionDockerPauseJob struct {
	EnqueueTime time.Time
	StartTime   time.Time
	Container   string
	DockerHost  string
	Retries     int
	Entity      ecs.Entity
}

func (j *InterventionDockerPauseJob) Execute(ctx context.Context) Result {
	return Result{Ent: j.Entity, Err: ErrDockerDisabled, Payload: GetInterventionDockerPayload()}
}
func (j *InterventionDockerPauseJob) Copy() Job                  { job := *j; return &job }
func (j *InterventionDockerPauseJob) GetEnqueueTime() time.Time  { return j.EnqueueTime }
func (j *InterventionDockerPauseJob) SetEnqueueTime(t time.Time) { j.EnqueueTime = t }
func (j *InterventionDockerPauseJob) GetStartTime() time.Time    { return j.StartTime }
func (j *InterventionDockerPauseJob) SetStartTime(t time.Time)   { j.StartTime = t }
func (j *InterventionDockerPauseJob) IsNil() bool                { return j == nil }

// InterventionDockerUnpauseJob is a stub when Docker is disabled.
type InterventionDockerUnpauseJob struct {
	EnqueueTime time.Time
	StartTime   time.Time
	Container   string
	DockerHost  string
	Retries     int
	Entity      ecs.Entity
}

func (j *InterventionDockerUnpauseJob) Execute(ctx context.Context) Result {
	return Result{Ent: j.Entity, Err: ErrDockerDisabled, Payload: GetInterventionDockerPayload()}
}
func (j *InterventionDockerUnpauseJob) Copy() Job                  { job := *j; return &job }
func (j *InterventionDockerUnpauseJob) GetEnqueueTime() time.Time  { return j.EnqueueTime }
func (j *InterventionDockerUnpauseJob) SetEnqueueTime(t time.Time) { j.EnqueueTime = t }
func (j *InterventionDockerUnpauseJob) GetStartTime() time.Time    { return j.StartTime }
func (j *InterventionDockerUnpauseJob) SetStartTime(t time.Time)   { j.StartTime = t }
func (j *InterventionDockerUnpauseJob) IsNil() bool                { return j == nil }

// InterventionDockerScaleJob is a stub when Docker is disabled.
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

func (j *InterventionDockerScaleJob) Execute(ctx context.Context) Result {
	return Result{Ent: j.Entity, Err: ErrDockerDisabled, Payload: GetInterventionDockerPayload()}
}
func (j *InterventionDockerScaleJob) Copy() Job                  { job := *j; return &job }
func (j *InterventionDockerScaleJob) GetEnqueueTime() time.Time  { return j.EnqueueTime }
func (j *InterventionDockerScaleJob) SetEnqueueTime(t time.Time) { j.EnqueueTime = t }
func (j *InterventionDockerScaleJob) GetStartTime() time.Time    { return j.StartTime }
func (j *InterventionDockerScaleJob) SetStartTime(t time.Time)   { j.StartTime = t }
func (j *InterventionDockerScaleJob) IsNil() bool                { return j == nil }





