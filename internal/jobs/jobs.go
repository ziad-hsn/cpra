package jobs

import (
	"cpra/internal/loader/schema"
	"fmt"
	"github.com/mlange-42/arche/ecs"
	"time"
)

type Job interface {
	Execute() Result
}

func CreatePulseJob(pulseSchema schema.Pulse, jobID ecs.Entity) (Job, error) {
	// Common parameters from schema.Pulse that are relevant for job execution
	timeout := pulseSchema.Timeout

	switch cfg := pulseSchema.Config.(type) { // cfg is the specific *schema.PulseHTTPConfig, etc.
	case *schema.PulseHTTPConfig:
		return &PulseHTTPJob{
			ID:      jobID,
			URL:     cfg.Url,
			Method:  cfg.Method, // Consider defaulting if empty
			Timeout: timeout,
			Retries: cfg.Retries,
		}, nil
	case *schema.PulseTCPConfig:
		return &PulseTCPJob{
			ID:      jobID,
			Host:    cfg.Host,
			Port:    cfg.Port,
			Timeout: timeout,
			Retries: cfg.Retries,
		}, nil
	case *schema.PulseICMPConfig:
		return &PulseICMPJob{
			ID:      jobID,
			Host:    cfg.Host,
			Timeout: timeout,
			Count:   cfg.Count,
		}, nil
	default:
		return nil, fmt.Errorf("unknown pulse config type: %T for job creation", pulseSchema.Config)
	}
}

type PulseHTTPJob struct {
	ID      ecs.Entity
	URL     string
	Method  string
	Timeout time.Duration
	Retries int
}

func (j *PulseHTTPJob) Execute() Result {
	fmt.Println("executing HTTP Job")
	res := PulseResults{ID: j.ID, Err: nil}
	return res
}

type PulseTCPJob struct {
	ID      ecs.Entity
	Host    string
	Port    int
	Timeout time.Duration
	Retries int
}

func (j *PulseTCPJob) Execute() Result {
	fmt.Println("executing TCP Job")
	res := PulseResults{ID: j.ID, Err: nil}
	return res
}

type PulseICMPJob struct {
	ID      ecs.Entity
	Host    string
	Count   int
	Timeout time.Duration
}

func (j *PulseICMPJob) Execute() Result {
	fmt.Println("executing ICMP Job")
	res := PulseResults{ID: j.ID, Err: fmt.Errorf("ICMP check failed")}
	return res
}
