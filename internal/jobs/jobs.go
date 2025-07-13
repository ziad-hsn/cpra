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

func (p *PulseHTTPJob) Execute() Result {
	fmt.Println("executing HTTP Job")
	time.Sleep(1 * time.Second)
	res := PulseResults{ID: p.ID, Err: fmt.Errorf("HTTP check failed")}
	return res
}
func (p *PulseHTTPJob) Copy() Job {
	// Create a new struct and copy all the values.
	return &PulseHTTPJob{
		ID:      p.ID,
		URL:     p.URL,
		Method:  p.Method,
		Timeout: p.Timeout,
		Retries: p.Retries,
	}

}

type PulseTCPJob struct {
	ID      ecs.Entity
	Host    string
	Port    int
	Timeout time.Duration
	Retries int
}

func (p *PulseTCPJob) Execute() Result {
	fmt.Println("executing TCP Job")
	res := PulseResults{ID: p.ID, Err: nil}
	return res
}

func (p *PulseTCPJob) Copy() Job {
	// Create a new struct and copy all the values.
	return &PulseTCPJob{
		ID:      p.ID,
		Host:    p.Host,
		Port:    p.Port,
		Timeout: p.Timeout,
		Retries: p.Retries,
	}

}

type PulseICMPJob struct {
	ID      ecs.Entity
	Host    string
	Count   int
	Timeout time.Duration
}

func (p *PulseICMPJob) Execute() Result {
	fmt.Println("executing ICMP Job")
	res := PulseResults{ID: p.ID, Err: fmt.Errorf("ICMP check failed\n")}
	return res
}

func (p *PulseICMPJob) Copy() Job {
	// Create a new struct and copy all the values.
	return &PulseICMPJob{
		ID:      p.ID,
		Host:    p.Host,
		Count:   p.Count,
		Timeout: p.Timeout,
	}

}

type InterventionDockerJob struct {
	ID        ecs.Entity
	Container string
	Timeout   time.Duration
	Retries   int
}

func (i *InterventionDockerJob) Execute() Result {
	fmt.Println("executing docker intervention Job")
	res := InterventionResults{ID: i.ID, Err: fmt.Errorf("Docker intervention failed\n")}
	return res
}
func (i *InterventionDockerJob) Copy() Job {
	// Create a new struct and copy all the values.
	return &InterventionDockerJob{
		ID:        i.ID,
		Container: i.Container,
		Timeout:   i.Timeout,
		Retries:   i.Retries,
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

func (c *CodeLogJob) Copy() Job {
	// Create a new struct and copy all the values.
	return &CodeLogJob{
		ID:      c.ID,
		File:    c.File,
		Timeout: c.Timeout,
		Retries: c.Retries,
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
func (c *CodeSlackJob) Copy() Job {
	// Create a new struct and copy all the values.
	return &CodeSlackJob{
		ID:      c.ID,
		WebHook: c.WebHook,
		Timeout: c.Timeout,
		Retries: c.Retries,
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

func (c *CodePagerDutyJob) Copy() Job {
	// Create a new struct and copy all the values.
	return &CodePagerDutyJob{
		ID:      c.ID,
		URL:     c.URL,
		Timeout: c.Timeout,
		Retries: c.Retries,
	}

}
