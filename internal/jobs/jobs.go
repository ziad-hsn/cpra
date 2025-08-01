package jobs

import (
	"cpra/internal/loader/schema"
	"fmt"
	"github.com/google/uuid"
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

	switch cfg := pulseSchema.Config.Copy().(type) { // cfg is the specific *schema.PulseHTTPConfig, etc.
	case *schema.PulseHTTPConfig:
		return &PulseHTTPJob{
			ID:      uuid.New(),
			Entity:  jobID,
			URL:     string([]byte(cfg.Url)),
			Method:  string([]byte(cfg.Method)), // Consider defaulting if empty
			Timeout: timeout,
			Retries: cfg.Retries,
		}, nil
	case *schema.PulseTCPConfig:
		return &PulseTCPJob{
			ID:      uuid.New(),
			Entity:  jobID,
			Host:    string([]byte(cfg.Host)),
			Port:    cfg.Port,
			Timeout: timeout,
			Retries: cfg.Retries,
		}, nil
	case *schema.PulseICMPConfig:
		return &PulseICMPJob{
			ID:      uuid.New(),
			Entity:  jobID,
			Host:    string([]byte(cfg.Host)),
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
			ID:        uuid.New(),
			Entity:    jobID,
			Container: string([]byte(InterventionSchema.Target.Copy().(*schema.InterventionTargetDocker).Container)),
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
		return &CodeLogJob{
			ID:      uuid.New(),
			File:    string([]byte(config.Config.Copy().(*schema.CodeNotificationLog).File)),
			Entity:  jobID,
			Monitor: string([]byte(monitor)),
			Message: fmt.Sprintf("%s monitor is down color and will send log alert.\n", string([]byte(monitor))),
		}, nil
	case "pagerduty":
		return &CodePagerDutyJob{
			ID:      uuid.New(),
			URL:     string([]byte(config.Config.Copy().(*schema.CodeNotificationPagerDuty).URL)),
			Entity:  jobID,
			Monitor: string([]byte(monitor)),
			Message: fmt.Sprintf("%s monitor is down color and will pagerduty slack alert.\n", string([]byte(monitor))),
		}, nil
	case "slack":
		return &CodeSlackJob{
			ID:      uuid.New(),
			WebHook: string([]byte(config.Config.Copy().(*schema.CodeNotificationSlack).WebHook)),
			Entity:  jobID,
			Monitor: string([]byte(monitor)),
			Message: fmt.Sprintf("%s monitor is down color and will send slack alert.\n", string([]byte(monitor))),
		}, nil

	default:
		return nil, fmt.Errorf("unknown code notification type: %T for job creation", config.Notify)

	}
}

type PulseHTTPJob struct {
	ID      uuid.UUID
	Entity  ecs.Entity
	URL     string
	Method  string
	Timeout time.Duration
	Retries int
}

func (p *PulseHTTPJob) Execute() Result {
	fmt.Println("executing HTTP Job")
	time.Sleep(time.Second / 2)
	res := Result{
		Ent: p.Entity,
		Err: fmt.Errorf("HTTP check failed"),
		ID:  p.ID,
	}
	return res
}
func (p *PulseHTTPJob) Copy() Job {
	// Create a new struct and copy all the values.
	return &PulseHTTPJob{
		ID:      p.ID,
		Entity:  p.Entity,
		URL:     p.URL,
		Method:  p.Method,
		Timeout: p.Timeout,
		Retries: p.Retries,
	}

}

type PulseTCPJob struct {
	ID      uuid.UUID
	Entity  ecs.Entity
	Host    string
	Port    int
	Timeout time.Duration
	Retries int
}

func (p *PulseTCPJob) Execute() Result {
	fmt.Println("executing TCP Job")
	res := Result{
		Ent: p.Entity,
		Err: nil,
		ID:  p.ID,
	}
	return res
}

func (p *PulseTCPJob) Copy() Job {
	// Create a new struct and copy all the values.
	return &PulseTCPJob{
		ID:      p.ID,
		Entity:  p.Entity,
		Host:    p.Host,
		Port:    p.Port,
		Timeout: p.Timeout,
		Retries: p.Retries,
	}

}

type PulseICMPJob struct {
	ID      uuid.UUID
	Entity  ecs.Entity
	Host    string
	Count   int
	Timeout time.Duration
}

func (p *PulseICMPJob) Execute() Result {
	fmt.Println("executing ICMP Job")
	res := Result{
		Ent: p.Entity,
		Err: fmt.Errorf("ICMP check failed\n"),
		ID:  p.ID,
	}
	return res
}

func (p *PulseICMPJob) Copy() Job {
	// Create a new struct and copy all the values.
	return &PulseICMPJob{
		ID:      p.ID,
		Entity:  p.Entity,
		Host:    p.Host,
		Count:   p.Count,
		Timeout: p.Timeout,
	}

}

type InterventionDockerJob struct {
	ID        uuid.UUID
	Entity    ecs.Entity
	Container string
	Timeout   time.Duration
	Retries   int
}

func (i *InterventionDockerJob) Execute() Result {
	fmt.Println("executing docker intervention Job")
	res := Result{
		Ent: i.Entity,
		Err: fmt.Errorf("Docker intervention failed\n"),
		ID:  i.ID,
	}
	return res
}
func (i *InterventionDockerJob) Copy() Job {
	// Create a new struct and copy all the values.
	return &InterventionDockerJob{
		ID:        i.ID,
		Entity:    i.Entity,
		Container: i.Container,
		Timeout:   i.Timeout,
		Retries:   i.Retries,
	}

}

type CodeLogJob struct {
	ID      uuid.UUID
	Entity  ecs.Entity
	File    string
	Message string
	Monitor string
	Timeout time.Duration
	Retries int
}

func (c *CodeLogJob) Execute() Result {
	fmt.Println("executing code Log Job")
	res := Result{
		Ent: c.Entity,
		Err: fmt.Errorf("Docker intervention failed\n"),
		ID:  c.ID,
	}
	return res
}

func (c *CodeLogJob) Copy() Job {
	// Create a new struct and copy all the values.
	return &CodeLogJob{
		ID:      c.ID,
		Entity:  c.Entity,
		File:    c.File,
		Message: c.Message,
		Monitor: c.Monitor,
		Timeout: c.Timeout,
		Retries: c.Retries,
	}

}

type CodeSlackJob struct {
	ID      uuid.UUID
	Entity  ecs.Entity
	WebHook string
	Message string
	Monitor string
	Timeout time.Duration
	Retries int
}

func (c *CodeSlackJob) Execute() Result {
	fmt.Println("executing code Log Job")
	res := Result{
		Ent: c.Entity,
		Err: fmt.Errorf("Docker intervention failed\n"),
		ID:  c.ID,
	}
	return res
}
func (c *CodeSlackJob) Copy() Job {
	// Create a new struct and copy all the values.
	return &CodeSlackJob{
		ID:      c.ID,
		Entity:  c.Entity,
		WebHook: c.WebHook,
		Message: c.Message,
		Monitor: c.Monitor,
		Timeout: c.Timeout,
		Retries: c.Retries,
	}

}

type CodePagerDutyJob struct {
	ID      uuid.UUID
	Entity  ecs.Entity
	URL     string
	Message string
	Monitor string
	Timeout time.Duration
	Retries int
}

func (c *CodePagerDutyJob) Execute() Result {
	fmt.Println("executing code pagerduty Job")
	res := Result{
		Ent: c.Entity,
		Err: fmt.Errorf("Docker intervention failed\n"),
		ID:  c.ID,
	}
	return res
}

func (c *CodePagerDutyJob) Copy() Job {
	// Create a new struct and copy all the values.
	return &CodePagerDutyJob{
		ID:      c.ID,
		Entity:  c.Entity,
		URL:     c.URL,
		Message: c.Message,
		Monitor: c.Monitor,
		Timeout: c.Timeout,
		Retries: c.Retries,
	}

}
