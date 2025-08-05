package jobs

import (
	"context"
	"cpra/internal/loader/schema"
	"fmt"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/google/uuid"
	"github.com/mlange-42/arche/ecs"
	"log"
	"net/http"
	"os"
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
	case *schema.PulseHTTPConfig:
		return &PulseHTTPJob{
			ID:      uuid.New(),
			Entity:  jobID,
			URL:     cfg.Url,
			Method:  cfg.Method, // Consider defaulting if empty
			Timeout: timeout,
			Retries: cfg.Retries,
			Client:  http.Client{Timeout: timeout},
		}, nil
	case *schema.PulseTCPConfig:
		return &PulseTCPJob{
			ID:      uuid.New(),
			Entity:  jobID,
			Host:    cfg.Host,
			Port:    cfg.Port,
			Timeout: timeout,
			Retries: cfg.Retries,
		}, nil
	case *schema.PulseICMPConfig:
		return &PulseICMPJob{
			ID:      uuid.New(),
			Entity:  jobID,
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
			ID:        uuid.New(),
			Entity:    jobID,
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
		return &CodeLogJob{
			ID:      uuid.New(),
			File:    config.Config.(*schema.CodeNotificationLog).File,
			Entity:  jobID,
			Monitor: monitor,
			Message: fmt.Sprintf("%s monitor is down color and will send log alert.\n", monitor),
		}, nil
	case "pagerduty":
		return &CodePagerDutyJob{
			ID:      uuid.New(),
			URL:     config.Config.(*schema.CodeNotificationPagerDuty).URL,
			Entity:  jobID,
			Monitor: monitor,
			Message: fmt.Sprintf("%s monitor is down color and will pagerduty slack alert.\n", monitor),
		}, nil
	case "slack":
		return &CodeSlackJob{
			ID:      uuid.New(),
			WebHook: config.Config.(*schema.CodeNotificationSlack).WebHook,
			Entity:  jobID,
			Monitor: monitor,
			Message: fmt.Sprintf("%s monitor is down color and will send slack alert.\n", monitor),
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
	Client  http.Client
	Retries int
}

// Execute performs the HTTP request for the job, with retries.
// It returns a Result indicating success (Err is nil) or failure.
func (p *PulseHTTPJob) Execute() Result {
	var lastErr error

	// Total attempts = 1 initial try + p.Retries
	attempts := p.Retries + 1
	for i := 0; i < attempts; i++ {
		// Create a new request for each attempt.
		req, err := http.NewRequest(p.Method, p.URL, nil)
		if err != nil {
			// This is a fatal error in creating the request itself; retrying won't help.
			return Result{
				ID:  p.ID,
				Ent: p.Entity,
				Err: fmt.Errorf("failed to create http request: %w", err),
			}
		}

		// Execute the request using the job's pre-configured client.
		resp, err := p.Client.Do(req)
		if err != nil {
			// Network error (e.g., timeout, DNS failure, connection refused).
			lastErr = err
			time.Sleep(50 * time.Millisecond) // wait briefly before retrying
			continue
		}

		// A successful response is typically in the 2xx range.
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			resp.Body.Close() // Success! Close the body and return.
			return Result{
				ID:  p.ID,
				Ent: p.Entity,
				Err: nil, // nil error indicates success
			}
		}

		// The server responded, but with a non-successful status code.
		lastErr = fmt.Errorf("received non-2xx status code: %s", resp.Status)
		err = resp.Body.Close()
		if err != nil {
			return Result{
				ID:  p.ID,
				Ent: p.Entity,
				Err: fmt.Errorf("failed to close http body: %w", err),
			}
		} // Always close the body to prevent resource leaks.
	}

	// If the loop finishes, all attempts have failed.
	return Result{
		ID:  p.ID,
		Ent: p.Entity,
		Err: fmt.Errorf("http check failed after %d attempt(s): %w", attempts, lastErr),
	}
}

func (p *PulseHTTPJob) Copy() Job {
	// Create a new struct and copy all the values.
	job := new(PulseHTTPJob)
	*job = *p
	return job
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
	job := new(PulseTCPJob)
	*job = *p
	return job

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
	job := new(PulseICMPJob)
	*job = *p
	return job

}

type InterventionDockerJob struct {
	ID        uuid.UUID
	Entity    ecs.Entity
	Container string
	Timeout   time.Duration
	Retries   int
}

// Execute performs the Docker intervention by restarting the specified container.
// It respects the configured timeout and number of retries.
func (i *InterventionDockerJob) Execute() Result {
	// Initialize a new Docker client from standard environment variables.
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return Result{
			ID:  i.ID,
			Ent: i.Entity,
			Err: fmt.Errorf("failed to create docker client: %w", err),
		}
	}
	// Ensure the client connection is closed when the function exits.
	defer cli.Close()

	var lastErr error

	// Total attempts = 1 initial try + i.Retries
	attempts := i.Retries + 1
	for attempt := 0; attempt < attempts; attempt++ {
		// Create a new context with a deadline for this specific attempt.
		ctx, cancel := context.WithTimeout(context.Background(), i.Timeout)
		defer cancel() // Important to prevent context leaks

		// The "intervention" is to restart the container.
		// We pass nil for the restart timeout, which makes Docker use the default (10 seconds).
		restartOptions := container.StopOptions{}
		err := cli.ContainerRestart(ctx, i.Container, restartOptions)
		if err == nil {
			// Success! The container was restarted.
			return Result{
				ID:  i.ID,
				Ent: i.Entity,
				Err: nil, // A nil error signifies success.
			}
		}

		// The restart failed (e.g., container not found, timeout exceeded).
		lastErr = err
	}

	// If the loop completes, all retries have failed.
	return Result{
		ID:  i.ID,
		Ent: i.Entity,
		Err: fmt.Errorf("docker intervention on '%s' failed after %d attempt(s): %w", i.Container, attempts, lastErr),
	}
}

func (i *InterventionDockerJob) Copy() Job {
	// Create a new struct and copy all the values.
	job := new(InterventionDockerJob)
	*job = *i
	return job

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

// Execute writes a formatted log message to a file.
// It handles retries and enforces a timeout on each write attempt.
func (c *CodeLogJob) Execute() Result {
	var lastErr error

	// Total attempts = 1 initial try + c.Retries
	attempts := c.Retries + 1
	for attempt := 0; attempt < attempts; attempt++ {
		// A channel to receive the outcome (error or nil) from the write operation.
		done := make(chan error, 1)

		// We run the file I/O in a separate goroutine. This allows the main
		// goroutine to use a `select` statement to enforce a timeout.
		go func() {
			// Open the file for appending. Create it with standard permissions if it doesn't exist.
			f, err := os.OpenFile(c.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				done <- err // Send error to the channel
				return
			}
			defer f.Close()

			// Format a structured log line including a timestamp, the monitor name, and the message.
			logLine := fmt.Sprintf(
				"%s [%s] %s\n",
				time.Now().UTC().Format(time.RFC3339), // ISO 8601 / RFC3339 timestamp
				c.Monitor,
				c.Message,
			)

			// Write the final string to the file.
			if _, err := f.WriteString(logLine); err != nil {
				done <- err // Send error to the channel
				return
			}
			log.Printf("logged code to %s for monitor %s\n", c.File, c.Monitor)

			// Signal success by sending a nil error.
			done <- nil
		}()

		// The select statement will wait for one of two events to occur first.
		select {
		case err := <-done:
			// The write operation finished.
			if err == nil {
				// Success! The log was written.
				return Result{
					ID:  c.ID,
					Ent: c.Entity,
					Err: nil,
				}
			}
			// The write operation failed.
			lastErr = err

		case <-time.After(c.Timeout):
			// The timeout duration passed before the write operation completed.
			lastErr = fmt.Errorf("write operation timed out after %v", c.Timeout)
		}
	}

	// If the loop finishes, all retries have failed.
	return Result{
		ID:  c.ID,
		Ent: c.Entity,
		Err: fmt.Errorf("log job for file '%s' failed after %d attempt(s): %w", c.File, attempts, lastErr),
	}
}

func (c *CodeLogJob) Copy() Job {
	// Create a new struct and copy all the values.
	job := new(CodeLogJob)
	*job = *c
	return job

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
	job := new(CodeSlackJob)
	*job = *c
	return job

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
	job := new(CodePagerDutyJob)
	*job = *c
	return job

}
