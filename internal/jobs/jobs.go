package jobs

import (
	"cpra/internal/loader/schema"
	"fmt"
	"github.com/mlange-42/arche/ecs"
	"time"
)

type Job interface {
	Execute() Result
	Copy() Job
}

func CreatePulseJob(pulseSchema schema.Pulse, jobID ecs.Entity) (Job, error) {
	// Common parameters from schema.Pulse that are relevant for job execution
	timeout := pulseSchema.Timeout

	switch cfg := pulseSchema.Config.(type) { // cfg is the specific *schema.PulseHTTPConfig, etc.
	case schema.PulseHTTPConfig:
		return &PulseHTTPJob{
			ID:      jobID,
			URL:     cfg.Url,
			Method:  cfg.Method, // Consider defaulting if empty
			Timeout: timeout,
			Retries: cfg.Retries,
		}, nil
	case schema.PulseTCPConfig:
		return &PulseTCPJob{
			ID:      jobID,
			Host:    cfg.Host,
			Port:    cfg.Port,
			Timeout: timeout,
			Retries: cfg.Retries,
		}, nil
	case schema.PulseICMPConfig:
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

func CreateInterventionJob(InterventionSchema schema.Intervention, jobID ecs.Entity) (Job, error) {
	// Common parameters from schema.Pulse that are relevant for job execution

	retries := InterventionSchema.Retries
	switch InterventionSchema.Action { // cfg is the specific *schema.PulseHTTPConfig, etc.
	case "docker":
		return &InterventionDockerJob{
			ID:        jobID,
			Container: InterventionSchema.Target.(*schema.InterventionTargetDocker).Container,
			Retries:   retries,
		}, nil
	default:
		return nil, fmt.Errorf("unknown intervention action : %T for job creation", InterventionSchema.Action)
	}
}

func CreateCodeJob(monitor string, config schema.CodeConfig, jobID ecs.Entity) (Job, error) {
	// Common parameters from schema.Pulse that are relevant for job execution
	switch config.Notify {
	case "log":
		return &CodeLogJob{File: config.Config.(*schema.CodeNotificationLog).File, ID: jobID, Monitor: monitor, Message: fmt.Sprintf("%s monitor is down color and will send log alert.\n", monitor)}, nil
	case "pagerduty":
		return &CodePagerDutyJob{URL: config.Config.(*schema.CodeNotificationPagerDuty).URL, ID: jobID, Monitor: monitor, Message: fmt.Sprintf("%s monitor is down color and will pagerduty slack alert.\n", monitor)}, nil
	case "slack":
		return &CodeSlackJob{WebHook: config.Config.(*schema.CodeNotificationSlack).WebHook, ID: jobID, Monitor: monitor, Message: fmt.Sprintf("%s monitor is down color and will send slack alert.\n", monitor)}, nil

	default:
		return nil, fmt.Errorf("unknown code notification type: %T for job creation", config.Notify)

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
	res := PulseResults{ID: j.ID, Err: fmt.Errorf("HTTP check failed")}
	return res
}
func (j *PulseHTTPJob) Copy() Job {
	// Create a new struct and copy all the values.
	return &PulseHTTPJob{
		ID:      j.ID,
		URL:     j.URL,
		Method:  j.Method,
		Timeout: j.Timeout,
		Retries: j.Retries,
	}

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

func (j *PulseTCPJob) Copy() Job {
	// Create a new struct and copy all the values.
	return &PulseTCPJob{
		ID:      j.ID,
		Host:    j.Host,
		Port:    j.Port,
		Timeout: j.Timeout,
		Retries: j.Retries,
	}

}

type PulseICMPJob struct {
	ID      ecs.Entity
	Host    string
	Count   int
	Timeout time.Duration
}

func (j *PulseICMPJob) Execute() Result {
	fmt.Println("executing ICMP Job")
	res := PulseResults{ID: j.ID, Err: fmt.Errorf("ICMP check failed\n")}
	return res
}

func (j *PulseICMPJob) Copy() Job {
	// Create a new struct and copy all the values.
	return &PulseICMPJob{
		ID:      j.ID,
		Host:    j.Host,
		Count:   j.Count,
		Timeout: j.Timeout,
	}

}

type InterventionDockerJob struct {
	ID        ecs.Entity
	Container string
	Timeout   time.Duration
	Retries   int
}

func (j *InterventionDockerJob) Execute() Result {
	fmt.Println("executing docker intervention Job")
	res := InterventionResults{ID: j.ID, Err: fmt.Errorf("Docker intervention failed\n")}
	return res
}
func (j *InterventionDockerJob) Copy() Job {
	// Create a new struct and copy all the values.
	return &InterventionDockerJob{
		ID:        j.ID,
		Container: j.Container,
		Timeout:   j.Timeout,
		Retries:   j.Retries,
	}

}

type CodeLogJob struct {
	ID      ecs.Entity
	File    string
	Message string
	Monitor string
	Timeout time.Duration
	Retries int
}

func (c *CodeLogJob) Execute() Result {
	fmt.Println("executing code Log Job")
	res := CodeResults{ID: c.ID, Err: fmt.Errorf("Docker intervention failed\n")}
	return res
}

func (j *CodeLogJob) Copy() Job {
	// Create a new struct and copy all the values.
	return &CodeLogJob{
		ID:      j.ID,
		File:    j.File,
		Timeout: j.Timeout,
		Retries: j.Retries,
	}

}

type CodeSlackJob struct {
	ID      ecs.Entity
	WebHook string
	Message string
	Monitor string
	Timeout time.Duration
	Retries int
}

func (c *CodeSlackJob) Execute() Result {
	fmt.Println("executing code Log Job")
	res := CodeResults{ID: c.ID, Err: fmt.Errorf("Docker intervention failed\n")}
	return res
}
func (j *CodeSlackJob) Copy() Job {
	// Create a new struct and copy all the values.
	return &CodeSlackJob{
		ID:      j.ID,
		WebHook: j.WebHook,
		Timeout: j.Timeout,
		Retries: j.Retries,
	}

}

type CodePagerDutyJob struct {
	ID      ecs.Entity
	URL     string
	Message string
	Monitor string
	Timeout time.Duration
	Retries int
}

func (c *CodePagerDutyJob) Execute() Result {
	fmt.Println("executing code pagerduty Job")
	res := CodeResults{ID: c.ID, Err: fmt.Errorf("Docker intervention failed\n")}
	return res
}

func (j *CodePagerDutyJob) Copy() Job {
	// Create a new struct and copy all the values.
	return &CodePagerDutyJob{
		ID:      j.ID,
		URL:     j.URL,
		Timeout: j.Timeout,
		Retries: j.Retries,
	}

}
