package components

import (
	"cpra/internal/jobs"
	"cpra/internal/loader/schema"
	"time"
)

type Name string

type DisabledMonitor struct{}

type MonitorStatus struct {
	Status          string
	LastCheckTime   time.Time
	LastSuccessTime time.Time
	LastError       error
}

type PulseConfig struct {
	Type        string
	Timeout     time.Duration
	Interval    time.Duration
	Retries     int
	MaxFailures int
	Config      schema.PulseConfig
}

type Pulse struct {
	Config PulseConfig
	Status PulseStatus
}
type PulseFirstCheck struct{}
type PulseNeeded struct{}
type PulsePending struct{}
type PulseFailed struct{}
type PulseSuccess struct{}

type PulseJob struct {
	Job jobs.Job
}

type PulseResults struct {
	Results jobs.Result
}

type PulseStatus struct {
	LastStatus          string
	ConsecutiveFailures int
	LastCheckTime       time.Time
	LastSuccessTime     time.Time
	LastError           error
}

type InterventionNeeded struct{}
type CodeNeeded struct {
	Color string
}

// Add Job and Result in an external package, add workerpool and workers in the package on its own, this separate ECS and workers communications without Circular Imports
type InterventionConfig struct {
	Action      string
	MaxFailures int
	Target      schema.InterventionTarget
}
type InterventionPending struct{}
type InterventionFailed struct{}
type InterventionSuccess struct{}

type InterventionJob struct {
	Job jobs.Job
}

type InterventionResults struct {
	Results jobs.Result
}

type InterventionStatus struct {
	LastStatus           string
	ConsecutiveFailures  int
	LastInterventionTime time.Time
	LastSuccessTime      time.Time
	LastError            error
}

// Add Job and Result in an external package, add workerpool and workers in the package on its own, this separate ECS and workers communications without Circular Imports
type CodeConfig struct {
	Dispatch    bool
	MaxFailures int
	Notify      string
	Config      schema.CodeNotification
}
type CodePending struct {
	Color string
}

type CodeJob struct {
	Job jobs.Job
}

type CodeResults struct {
	Results jobs.Result
}

type CodeStatus struct {
	LastStatus          string
	ConsecutiveFailures int
	LastAlertTime       time.Time
	LastSuccessTime     time.Time
	LastError           error
}

type CodeStatusAccessor interface {
	SetSuccess(t time.Time)
	SetFailure(err error)
}

// Marker/tag components
type RedCode struct{} // use when an entity is a "red code"

type RedCodeJob struct {
	Job jobs.Job
}
type RedCodeConfig struct {
	Dispatch    bool
	MaxFailures int
	Notify      string
	Config      schema.CodeNotification
}
type RedCodeStatus struct {
	LastStatus          string
	ConsecutiveFailures int
	LastAlertTime       time.Time
	LastSuccessTime     time.Time
	LastError           error
}

func (s *RedCodeStatus) SetSuccess(t time.Time) {
	s.LastStatus = "success"
	s.LastError = nil
	s.ConsecutiveFailures = 0
	s.LastSuccessTime = t
	s.LastAlertTime = t
}

func (s *RedCodeStatus) SetFailure(err error) {
	s.LastStatus = "failed"
	s.LastError = err
	s.ConsecutiveFailures++
}

type GreenCode struct{} // etc.

type GreenCodeJob struct {
	Job jobs.Job
}
type GreenCodeConfig struct {
	Dispatch    bool
	MaxFailures int
	Notify      string
	Config      schema.CodeNotification
}
type GreenCodeStatus struct {
	LastStatus          string
	ConsecutiveFailures int
	LastAlertTime       time.Time
	LastSuccessTime     time.Time
	LastError           error
}

func (s *GreenCodeStatus) SetSuccess(t time.Time) {
	s.LastStatus = "success"
	s.LastError = nil
	s.ConsecutiveFailures = 0
	s.LastSuccessTime = t
	s.LastAlertTime = t
}

func (s *GreenCodeStatus) SetFailure(err error) {
	s.LastStatus = "failed"
	s.LastError = err
	s.ConsecutiveFailures++
}

type CyanCode struct{}

type CyanCodeJob struct {
	Job jobs.Job
}
type CyanCodeConfig struct {
	Dispatch    bool
	MaxFailures int
	Notify      string
	Config      schema.CodeNotification
}
type CyanCodeStatus struct {
	LastStatus          string
	ConsecutiveFailures int
	LastAlertTime       time.Time
	LastSuccessTime     time.Time
	LastError           error
}

func (s *CyanCodeStatus) SetSuccess(t time.Time) {
	s.LastStatus = "success"
	s.LastError = nil
	s.ConsecutiveFailures = 0
	s.LastSuccessTime = t
	s.LastAlertTime = t
}

func (s *CyanCodeStatus) SetFailure(err error) {
	s.LastStatus = "failed"
	s.LastError = err
	s.ConsecutiveFailures++
}

type YellowCode struct{}

type YellowCodeJob struct {
	Job jobs.Job
}
type YellowCodeConfig struct {
	Dispatch    bool
	MaxFailures int
	Notify      string
	Config      schema.CodeNotification
}
type YellowCodeStatus struct {
	LastStatus          string
	ConsecutiveFailures int
	LastAlertTime       time.Time
	LastSuccessTime     time.Time
	LastError           error
}

func (s *YellowCodeStatus) SetSuccess(t time.Time) {
	s.LastStatus = "success"
	s.LastError = nil
	s.ConsecutiveFailures = 0
	s.LastSuccessTime = t
	s.LastAlertTime = t
}

func (s *YellowCodeStatus) SetFailure(err error) {
	s.LastStatus = "failed"
	s.LastError = err
	s.ConsecutiveFailures++
}

// GrayCode TODO when API is implemented
type GrayCode struct{}

type GrayCodeJob struct {
	Job jobs.Job
}
type GrayCodeConfig struct {
	Dispatch    bool
	MaxFailures int
	Notify      string
	Config      schema.CodeNotification
}

type GrayCodeStatus struct {
	LastStatus          string
	ConsecutiveFailures int
	LastAlertTime       time.Time
	LastSuccessTime     time.Time
	LastError           error
}

func (s *GrayCodeStatus) SetSuccess(t time.Time) {
	s.LastStatus = "success"
	s.LastError = nil
	s.ConsecutiveFailures = 0
	s.LastSuccessTime = t
	s.LastAlertTime = t
}

func (s *GrayCodeStatus) SetFailure(err error) {
	s.LastStatus = "failed"
	s.LastError = err
	s.ConsecutiveFailures++
}
