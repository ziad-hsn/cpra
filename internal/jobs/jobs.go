package jobs

import (
	"context"
	"cpra/internal/loader/schema"
	"fmt"
	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"net/http"
	"os"
	"strings"
	"time"
)

// Job defines the interface for any executable task in the system.
type Job interface {
	Execute() Result
	Copy() Job
	GetEnqueueTime() time.Time
	SetEnqueueTime(time.Time)
	GetStartTime() time.Time
	SetStartTime(time.Time)
}

// CreatePulseJob creates a new pulse job based on the provided schema.
func CreatePulseJob(pulseSchema schema.Pulse, jobID ecs.Entity) (Job, error) {
	timeout := pulseSchema.Timeout
	switch cfg := pulseSchema.Config.(type) {
	case *schema.PulseHTTPConfig:
		return &PulseHTTPJob{
			ID:      uuid.New(),
			Entity:  jobID,
			URL:     strings.Clone(cfg.Url),
			Method:  strings.Clone(cfg.Method),
			Timeout: timeout,
			Retries: cfg.Retries,
			Client:  http.Client{Timeout: timeout},
		}, nil
	case *schema.PulseTCPConfig:
		return &PulseTCPJob{
			ID:      uuid.New(),
			Entity:  jobID,
			Host:    strings.Clone(cfg.Host),
			Port:    cfg.Port,
			Timeout: timeout,
			Retries: cfg.Retries,
		}, nil
	case *schema.PulseICMPConfig:
		return &PulseICMPJob{
			ID:      uuid.New(),
			Entity:  jobID,
			Host:    strings.Clone(cfg.Host),
			Timeout: timeout,
			Count:   cfg.Count,
		}, nil

	// ... other pulse job types
	default:
		return nil, fmt.Errorf("unknown pulse config type: %T for job creation", pulseSchema.Config)
	}
}

// CreateInterventionJob creates a new intervention job based on the provided schema.
func CreateInterventionJob(interventionSchema schema.Intervention, jobID ecs.Entity) (Job, error) {
	retries := interventionSchema.Retries
	switch interventionSchema.Action {
	case "docker":
		return &InterventionDockerJob{
			ID:        uuid.New(),
			Entity:    jobID,
			Container: strings.Clone(interventionSchema.Target.(*schema.InterventionTargetDocker).Container),
			Retries:   retries,
			Timeout:   interventionSchema.Target.(*schema.InterventionTargetDocker).Timeout,
		}, nil
	default:
		return nil, fmt.Errorf("unknown intervention action : %T for job creation", interventionSchema.Action)
	}
}

// CreateCodeJob creates a new code alert job based on the provided configuration.
func CreateCodeJob(monitor string, config schema.CodeConfig, jobID ecs.Entity, color string) (Job, error) {
	// ... message creation logic ...
	message := "..."
	switch config.Notify {
	case "log":
		return &CodeLogJob{
			ID:      uuid.New(),
			File:    strings.Clone(config.Config.(*schema.CodeNotificationLog).File),
			Entity:  jobID,
			Monitor: strings.Clone(monitor),
			Message: message,
			Color:   color,
		}, nil
	case "pagerduty":
		return &CodePagerDutyJob{
			ID:      uuid.New(),
			Entity:  jobID,
			Monitor: strings.Clone(monitor),
			Message: message,
			Color:   color,
		}, nil
	case "slack":
		return &CodeSlackJob{
			ID:      uuid.New(),
			Entity:  jobID,
			Monitor: strings.Clone(monitor),
			Message: message,
			Color:   color,
		}, nil
	case "email":
		return &CodeEmailJob{
			ID:      uuid.New(),
			Entity:  jobID,
			Monitor: strings.Clone(monitor),
			Message: message,
			Color:   color,
		}, nil
	case "webhook":
		return &CodeWebhookJob{
			ID:      uuid.New(),
			Entity:  jobID,
			Monitor: strings.Clone(monitor),
			Message: message,
			Color:   color,
		}, nil
	default:
		return nil, fmt.Errorf("unknown code notification type: %s for job creation", config.Notify)
	}
}

// --- Pulse Job Implementations ---

type PulseHTTPJob struct {
	ID          uuid.UUID
	Entity      ecs.Entity
	URL         string
	Method      string
	Timeout     time.Duration
	Client      http.Client
	Retries     int
	EnqueueTime time.Time
	StartTime   time.Time
}

func (p *PulseHTTPJob) Execute() Result {
	var lastErr error
	attempts := p.Retries + 1
	payload := map[string]interface{}{"type": "pulse"}

	for i := 0; i < attempts; i++ {
		req, err := http.NewRequest(p.Method, p.URL, nil)
		if err != nil {
			return Result{ID: p.ID, Ent: p.Entity, Err: fmt.Errorf("failed to create http request: %w", err), Payload: payload}
		}
		resp, err := p.Client.Do(req)
		if err != nil {
			lastErr = err
			time.Sleep(50 * time.Millisecond)
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return Result{ID: p.ID, Ent: p.Entity, Err: nil, Payload: payload}
		}
		lastErr = fmt.Errorf("received non-2xx status code: %s", resp.Status)
	}
	return Result{ID: p.ID, Ent: p.Entity, Err: fmt.Errorf("http check failed after %d attempt(s): %w", attempts, lastErr), Payload: payload}
}

func (p *PulseHTTPJob) Copy() Job                  { job := *p; return &job }
func (p *PulseHTTPJob) GetEnqueueTime() time.Time  { return p.EnqueueTime }
func (p *PulseHTTPJob) SetEnqueueTime(t time.Time) { p.EnqueueTime = t }
func (p *PulseHTTPJob) GetStartTime() time.Time    { return p.StartTime }
func (p *PulseHTTPJob) SetStartTime(t time.Time)   { p.StartTime = t }

// PulseTCPJob is a placeholder for a TCP pulse job.
type PulseTCPJob struct {
	ID          uuid.UUID
	Entity      ecs.Entity
	Host        string
	Port        int
	Timeout     time.Duration
	Retries     int
	EnqueueTime time.Time
	StartTime   time.Time
}

func (p *PulseTCPJob) Execute() Result {
	// Mock implementation: does nothing and succeeds.
	return Result{ID: p.ID, Ent: p.Entity, Err: nil, Payload: map[string]interface{}{"type": "pulse", "driver": "tcp"}}
}

func (p *PulseTCPJob) Copy() Job                  { job := *p; return &job }
func (p *PulseTCPJob) GetEnqueueTime() time.Time  { return p.EnqueueTime }
func (p *PulseTCPJob) SetEnqueueTime(t time.Time) { p.EnqueueTime = t }
func (p *PulseTCPJob) GetStartTime() time.Time    { return p.StartTime }
func (p *PulseTCPJob) SetStartTime(t time.Time)   { p.StartTime = t }

// PulseICMPJob is a placeholder for an ICMP pulse job.
type PulseICMPJob struct {
	ID          uuid.UUID
	Entity      ecs.Entity
	Host        string
	Timeout     time.Duration
	Count       int
	EnqueueTime time.Time
	StartTime   time.Time
}

func (p *PulseICMPJob) Execute() Result {
	// Mock implementation: does nothing and succeeds.
	return Result{ID: p.ID, Ent: p.Entity, Err: nil, Payload: map[string]interface{}{"type": "pulse", "driver": "icmp"}}
}

func (p *PulseICMPJob) Copy() Job                  { job := *p; return &job }
func (p *PulseICMPJob) GetEnqueueTime() time.Time  { return p.EnqueueTime }
func (p *PulseICMPJob) SetEnqueueTime(t time.Time) { p.EnqueueTime = t }
func (p *PulseICMPJob) GetStartTime() time.Time    { return p.StartTime }
func (p *PulseICMPJob) SetStartTime(t time.Time)   { p.StartTime = t }

// --- Intervention Job Implementations ---

type InterventionDockerJob struct {
	ID          uuid.UUID
	Entity      ecs.Entity
	Container   string
	Timeout     time.Duration
	Retries     int
	EnqueueTime time.Time
	StartTime   time.Time
}

func (i *InterventionDockerJob) Execute() Result {
	payload := map[string]interface{}{"type": "intervention"}
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return Result{ID: i.ID, Ent: i.Entity, Err: fmt.Errorf("failed to create docker client: %w", err), Payload: payload}
	}
	defer cli.Close()

	var lastErr error
	attempts := i.Retries + 1
	for attempt := 0; attempt < attempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), i.Timeout)
		defer cancel()
		timeout := int(i.Timeout.Seconds())
		restartOptions := container.StopOptions{Timeout: &timeout}
		err := cli.ContainerRestart(ctx, i.Container, restartOptions)
		if err == nil {
			return Result{ID: i.ID, Ent: i.Entity, Err: nil, Payload: payload}
		}
		lastErr = err
	}
	return Result{ID: i.ID, Ent: i.Entity, Err: fmt.Errorf("docker intervention on '%s' failed after %d attempt(s): %w", i.Container, attempts, lastErr), Payload: payload}
}

func (i *InterventionDockerJob) Copy() Job                  { job := *i; return &job }
func (i *InterventionDockerJob) GetEnqueueTime() time.Time  { return i.EnqueueTime }
func (i *InterventionDockerJob) SetEnqueueTime(t time.Time) { i.EnqueueTime = t }
func (i *InterventionDockerJob) GetStartTime() time.Time    { return i.StartTime }
func (i *InterventionDockerJob) SetStartTime(t time.Time)   { i.StartTime = t }

// --- Code Job Implementations ---

type CodeLogJob struct {
	ID          uuid.UUID
	Entity      ecs.Entity
	File        string
	Message     string
	Monitor     string
	Color       string
	EnqueueTime time.Time
	StartTime   time.Time
}

func (c *CodeLogJob) Execute() Result {
	payload := map[string]interface{}{"type": "code", "color": c.Color}
	f, err := os.OpenFile(c.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
	}
	defer f.Close()

	timestamp := time.Now().Format("2006-01-02 15:04:05.000 Z07:00")
	logLine := fmt.Sprintf("%s [%s] %s\n", timestamp, c.Monitor, c.Message)

	_, err = f.WriteString(logLine)
	return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
}

func (c *CodeLogJob) Copy() Job                  { job := *c; return &job }
func (c *CodeLogJob) GetEnqueueTime() time.Time  { return c.EnqueueTime }
func (c *CodeLogJob) SetEnqueueTime(t time.Time) { c.EnqueueTime = t }
func (c *CodeLogJob) GetStartTime() time.Time    { return c.StartTime }
func (c *CodeLogJob) SetStartTime(t time.Time)   { c.StartTime = t }

// CodePagerDutyJob is a placeholder for a PagerDuty notification job.
type CodePagerDutyJob struct {
	ID          uuid.UUID
	Entity      ecs.Entity
	Monitor     string
	Message     string
	Color       string
	EnqueueTime time.Time
	StartTime   time.Time
}

func (c *CodePagerDutyJob) Execute() Result {
	// Mock implementation: does nothing and succeeds.
	return Result{ID: c.ID, Ent: c.Entity, Err: nil, Payload: map[string]interface{}{"type": "code", "driver": "pagerduty", "color": c.Color}}
}

func (c *CodePagerDutyJob) Copy() Job                  { job := *c; return &job }
func (c *CodePagerDutyJob) GetEnqueueTime() time.Time  { return c.EnqueueTime }
func (c *CodePagerDutyJob) SetEnqueueTime(t time.Time) { c.EnqueueTime = t }
func (c *CodePagerDutyJob) GetStartTime() time.Time    { return c.StartTime }
func (c *CodePagerDutyJob) SetStartTime(t time.Time)   { c.StartTime = t }

// CodeSlackJob is a placeholder for a Slack notification job.
type CodeSlackJob struct {
	ID          uuid.UUID
	Entity      ecs.Entity
	Monitor     string
	Message     string
	Color       string
	EnqueueTime time.Time
	StartTime   time.Time
}

func (c *CodeSlackJob) Execute() Result {
	// Mock implementation: does nothing and succeeds.
	return Result{ID: c.ID, Ent: c.Entity, Err: nil, Payload: map[string]interface{}{"type": "code", "driver": "slack", "color": c.Color}}
}

func (c *CodeSlackJob) Copy() Job                  { job := *c; return &job }
func (c *CodeSlackJob) GetEnqueueTime() time.Time  { return c.EnqueueTime }
func (c *CodeSlackJob) SetEnqueueTime(t time.Time) { c.EnqueueTime = t }
func (c *CodeSlackJob) GetStartTime() time.Time    { return c.StartTime }
func (c *CodeSlackJob) SetStartTime(t time.Time)   { c.StartTime = t }

// CodeEmailJob is a placeholder for an email notification job.
type CodeEmailJob struct {
	ID          uuid.UUID
	Entity      ecs.Entity
	Monitor     string
	Message     string
	Color       string
	EnqueueTime time.Time
	StartTime   time.Time
}

func (c *CodeEmailJob) Execute() Result {
	// Mock implementation: does nothing and succeeds.
	return Result{ID: c.ID, Ent: c.Entity, Err: nil, Payload: map[string]interface{}{"type": "code", "driver": "email", "color": c.Color}}
}

func (c *CodeEmailJob) Copy() Job                  { job := *c; return &job }
func (c *CodeEmailJob) GetEnqueueTime() time.Time  { return c.EnqueueTime }
func (c *CodeEmailJob) SetEnqueueTime(t time.Time) { c.EnqueueTime = t }
func (c *CodeEmailJob) GetStartTime() time.Time    { return c.StartTime }
func (c *CodeEmailJob) SetStartTime(t time.Time)   { c.StartTime = t }

// CodeWebhookJob is a placeholder for a webhook notification job.
type CodeWebhookJob struct {
	ID          uuid.UUID
	Entity      ecs.Entity
	Monitor     string
	Message     string
	Color       string
	EnqueueTime time.Time
	StartTime   time.Time
}

func (c *CodeWebhookJob) Execute() Result {
	// Mock implementation: does nothing and succeeds.
	return Result{ID: c.ID, Ent: c.Entity, Err: nil, Payload: map[string]interface{}{"type": "code", "driver": "webhook", "color": c.Color}}
}

func (c *CodeWebhookJob) Copy() Job                  { job := *c; return &job }
func (c *CodeWebhookJob) GetEnqueueTime() time.Time  { return c.EnqueueTime }
func (c *CodeWebhookJob) SetEnqueueTime(t time.Time) { c.EnqueueTime = t }
func (c *CodeWebhookJob) GetStartTime() time.Time    { return c.StartTime }
func (c *CodeWebhookJob) SetStartTime(t time.Time)   { c.StartTime = t }
