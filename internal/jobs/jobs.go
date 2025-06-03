package jobs

import (
	"cpra/internal/loader/schema"
	"fmt"
	"time"
)

type Job interface {
	Execute()
}

func CreatePulseJob(pulseSchema schema.Pulse, jobID uint32) (Job, error) {
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
	ID      uint32
	URL     string
	Method  string
	Timeout time.Duration
	Retries int
}

func (j *PulseHTTPJob) Execute() {
	fmt.Println("executing HTTP Job")
}

type PulseTCPJob struct {
	ID      uint32
	Host    string
	Port    int
	Timeout time.Duration
	Retries int
}

func (j *PulseTCPJob) Execute() {
	fmt.Println("executing TCP Job")
}

type PulseICMPJob struct {
	ID      uint32
	Host    string
	Count   int
	Timeout time.Duration
}

func (j *PulseICMPJob) Execute() {
	fmt.Println("executing ICMP Job")
}
