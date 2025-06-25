package components

import (
	"cpra/internal/jobs"
	"cpra/internal/loader/schema"
	"time"
)

type Name string

type DisabledMonitor struct{}

type PulseConfig struct {
	Type        string
	Timeout     time.Duration
	Interval    time.Duration
	Retries     int
	MaxFailures int
	Config      schema.PulseConfig
}

type PulseFirstCheck struct{}
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
	LastJobID           uint32
	ConsecutiveFailures int
	LastCheckTime       time.Time
	LastSuccessTime     time.Time
	LastError           error
}

// Add Job and Result in an external package, add workerpool and workers in the package on its own, this separate ECS and workers communications without Circular Imports

type InterventionConfig struct {
	Action   string
	Cooldown int
	// ...
}

type CodeConfig struct {
	ID       string
	Color    string // Or you can use an enum
	Severity int
	// ...
}

// Marker/tag components
type RedCode struct{}    // use when an entity is a "red code"
type YellowCode struct{} // etc.

// Alternative: use a value type for color/marker
type CodeColor struct {
	Color string // "red", "yellow", ...
}
