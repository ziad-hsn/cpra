package jobs

import (
	"bytes"
	"context"
	"cpra/internal/loader/schema"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
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
    IsNil() bool
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
			ID:              uuid.New(),
			Entity:          jobID,
			Host:            strings.Clone(cfg.Host),
			Timeout:         timeout,
			Count:           cfg.Count,
			Retries:         cfg.Retries,
			IgnorePrivilege: cfg.Privilege,
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

type codeAlertTemplate struct {
	Title     string
	Status    string
	Severity  string
	Summary   string
	Action    string
	NextSteps string
}

func codeAlertTemplateFor(color string) codeAlertTemplate {
	switch strings.ToLower(color) {
	case "red":
		return codeAlertTemplate{
			Title:     "CRITICAL ALERT",
			Status:    "FAILED",
			Severity:  "critical",
			Summary:   "Service outage detected after repeated health check failures and interventions",
			Action:    "Escalate immediately and engage on-call responders",
			NextSteps: "Perform manual recovery and review related service telemetry",
		}
	case "yellow":
		return codeAlertTemplate{
			Title:     "DEGRADED ALERT",
			Status:    "DEGRADED",
			Severity:  "warning",
			Summary:   "Service health checks are failing consecutively beyond safe thresholds",
			Action:    "Investigate partial outage or performance regression",
			NextSteps: "Validate dependencies, review recent changes, and monitor closely",
		}
	case "green":
		return codeAlertTemplate{
			Title:     "RECOVERY NOTICE",
			Status:    "RECOVERED",
			Severity:  "info",
			Summary:   "Service returned to a healthy state after previous failures",
			Action:    "No immediate action required",
			NextSteps: "Continue monitoring stability and capture incident follow-up notes",
		}
	case "cyan":
		return codeAlertTemplate{
			Title:     "INTERVENTION SUCCESS",
			Status:    "RESTORED",
			Severity:  "info",
			Summary:   "Automated intervention completed and service health checks are passing",
			Action:    "Confirm downstream systems are stable",
			NextSteps: "Document intervention details and verify customer impact is resolved",
		}
	case "gray":
		return codeAlertTemplate{
			Title:     "MAINTENANCE MODE",
			Status:    "MAINTENANCE",
			Severity:  "info",
			Summary:   "Monitor is intentionally suppressed during planned maintenance",
			Action:    "No action required during maintenance window",
			NextSteps: "Re-enable monitoring once maintenance activities conclude",
		}
	default:
		return codeAlertTemplate{
			Title:     "STATUS UPDATE",
			Status:    "UNKNOWN",
			Severity:  "unknown",
			Summary:   "Monitor generated an unspecified status update",
			Action:    "Review monitor configuration and recent events",
			NextSteps: "Validate service state and adjust alert routing if required",
		}
	}
}

func buildCodeNotificationMessage(monitor string, tpl codeAlertTemplate) string {
	return fmt.Sprintf("%s\nMonitor: %s\nStatus: %s\nSeverity: %s\nSummary: %s\nRecommended Action: %s\nNext Steps: %s",
		tpl.Title,
		monitor,
		tpl.Status,
		strings.ToUpper(tpl.Severity),
		tpl.Summary,
		tpl.Action,
		tpl.NextSteps,
	)
}

// CreateCodeJob creates a new code alert job based on the provided configuration.
func CreateCodeJob(monitor string, config schema.CodeConfig, jobID ecs.Entity, color string) (Job, error) {
	template := codeAlertTemplateFor(color)
	message := buildCodeNotificationMessage(monitor, template)
	colorClone := strings.Clone(color)
	monitorClone := strings.Clone(monitor)

	switch config.Notify {
	case "log":
		return &CodeLogJob{
			ID:        uuid.New(),
			File:      strings.Clone(config.Config.(*schema.CodeNotificationLog).File),
			Entity:    jobID,
			Monitor:   monitorClone,
			Message:   message,
			Color:     colorClone,
			Status:    template.Status,
			Severity:  template.Severity,
			Summary:   template.Summary,
			Action:    template.Action,
			NextSteps: template.NextSteps,
		}, nil
	case "pagerduty":
		return &CodePagerDutyJob{
			ID:      uuid.New(),
			Entity:  jobID,
			Monitor: monitorClone,
			Message: message,
			Color:   colorClone,
		}, nil
	case "slack":
		return &CodeSlackJob{
			ID:      uuid.New(),
			Entity:  jobID,
			Monitor: monitorClone,
			Message: message,
			Color:   colorClone,
		}, nil
	case "email":
		return &CodeEmailJob{
			ID:      uuid.New(),
			Entity:  jobID,
			Monitor: monitorClone,
			Message: message,
			Color:   colorClone,
		}, nil
	case "webhook":
		return &CodeWebhookJob{
			ID:      uuid.New(),
			Entity:  jobID,
			Monitor: monitorClone,
			Message: message,
			Color:   colorClone,
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
		// Close response body immediately after checking status
		statusOk := resp.StatusCode >= 200 && resp.StatusCode < 300
		resp.Body.Close()
		if statusOk {
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
func (p *PulseHTTPJob) IsNil() bool                { return p == nil }

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
	payload := map[string]interface{}{"type": "pulse", "driver": "tcp"}
	attempts := p.Retries + 1
	if attempts < 1 {
		attempts = 1
	}

	address := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
	var lastErr error

	for attempt := 0; attempt < attempts; attempt++ {
		conn, err := net.DialTimeout("tcp", address, p.Timeout)
		if err == nil {
			_ = conn.SetDeadline(time.Now().Add(p.Timeout))
			_ = conn.Close()
			return Result{ID: p.ID, Ent: p.Entity, Err: nil, Payload: payload}
		}
		lastErr = err
		if attempt < attempts-1 {
			time.Sleep(50 * time.Millisecond)
		}
	}

	return Result{
		ID:      p.ID,
		Ent:     p.Entity,
		Err:     fmt.Errorf("tcp check failed for %s after %d attempt(s): %w", address, attempts, lastErr),
		Payload: payload,
	}
}

func (p *PulseTCPJob) Copy() Job                  { job := *p; return &job }
func (p *PulseTCPJob) GetEnqueueTime() time.Time  { return p.EnqueueTime }
func (p *PulseTCPJob) SetEnqueueTime(t time.Time) { p.EnqueueTime = t }
func (p *PulseTCPJob) GetStartTime() time.Time    { return p.StartTime }
func (p *PulseTCPJob) SetStartTime(t time.Time)   { p.StartTime = t }
func (p *PulseTCPJob) IsNil() bool                { return p == nil }

// PulseICMPJob is a placeholder for an ICMP pulse job.
type PulseICMPJob struct {
	ID              uuid.UUID
	Entity          ecs.Entity
	Host            string
	Timeout         time.Duration
	Count           int
	Retries         int
	IgnorePrivilege bool
	EnqueueTime     time.Time
	StartTime       time.Time
}

var errICMPPrivilege = errors.New("icmp requires elevated privileges")

func (p *PulseICMPJob) Execute() Result {
	payload := map[string]interface{}{"type": "pulse", "driver": "icmp"}
	attempts := p.Retries + 1
	if attempts < 1 {
		attempts = 1
	}

	count := p.Count
	if count <= 0 {
		count = 1
	}
	payload["count"] = count

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		err := p.runPingAttempt(count)
		if err == nil {
			return Result{ID: p.ID, Ent: p.Entity, Err: nil, Payload: payload}
		}
		if errors.Is(err, errICMPPrivilege) && p.IgnorePrivilege {
			payload["privilege_ignored"] = true
			return Result{ID: p.ID, Ent: p.Entity, Err: nil, Payload: payload}
		}

		lastErr = err
		if attempt < attempts-1 {
			time.Sleep(50 * time.Millisecond)
		}
	}

	if p.IgnorePrivilege && errors.Is(lastErr, errICMPPrivilege) {
		payload["privilege_ignored"] = true
		return Result{ID: p.ID, Ent: p.Entity, Err: nil, Payload: payload}
	}

	return Result{
		ID:      p.ID,
		Ent:     p.Entity,
		Err:     fmt.Errorf("icmp check failed for %s after %d attempt(s): %w", p.Host, attempts, lastErr),
		Payload: payload,
	}
}

func (p *PulseICMPJob) runPingAttempt(count int) error {
	attemptTimeout := p.Timeout
	if attemptTimeout <= 0 {
		attemptTimeout = time.Second
	}

	commandTimeout := attemptTimeout
	if count > 1 {
		minDuration := attemptTimeout + time.Duration(count-1)*time.Second
		if commandTimeout < minDuration {
			commandTimeout = minDuration
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	args := buildPingArgs(runtime.GOOS, p.Host, count, attemptTimeout)
	cmd := exec.CommandContext(ctx, "ping", args...)

	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("ping to %s timed out after %s", p.Host, commandTimeout)
		}

		var notFoundErr *exec.Error
		if errors.As(err, &notFoundErr) && errors.Is(notFoundErr.Err, exec.ErrNotFound) {
			return fmt.Errorf("ping binary not found in PATH")
		}

		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			output := strings.TrimSpace(combined.String())
			if isICMPPrivilegeError(output) {
				return errICMPPrivilege
			}
			if output == "" {
				return fmt.Errorf("ping exited with status %s", exitErr.ProcessState.String())
			}
			return fmt.Errorf("%s", output)
		}

		return err
	}

	return nil
}

func buildPingArgs(goos, host string, count int, perAttemptTimeout time.Duration) []string {
	if count <= 0 {
		count = 1
	}

	switch goos {
	case "windows":
		timeoutMS := int(perAttemptTimeout / time.Millisecond)
		if timeoutMS <= 0 {
			timeoutMS = int((time.Second).Milliseconds())
		}
		return []string{"-n", strconv.Itoa(count), "-w", strconv.Itoa(timeoutMS), host}
	default:
		return []string{"-c", strconv.Itoa(count), host}
	}
}

func isICMPPrivilegeError(output string) bool {
	if output == "" {
		return false
	}
	lower := strings.ToLower(output)
	return strings.Contains(lower, "operation not permitted") ||
		strings.Contains(lower, "permission denied") ||
		strings.Contains(lower, "privilege")
}

func (p *PulseICMPJob) Copy() Job                  { job := *p; return &job }
func (p *PulseICMPJob) GetEnqueueTime() time.Time  { return p.EnqueueTime }
func (p *PulseICMPJob) SetEnqueueTime(t time.Time) { p.EnqueueTime = t }
func (p *PulseICMPJob) GetStartTime() time.Time    { return p.StartTime }
func (p *PulseICMPJob) SetStartTime(t time.Time)   { p.StartTime = t }
func (p *PulseICMPJob) IsNil() bool                { return p == nil }

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
		timeout := int(i.Timeout.Seconds())
		restartOptions := container.StopOptions{Timeout: &timeout}
		err := cli.ContainerRestart(ctx, i.Container, restartOptions)
		cancel() // Clean up context immediately after use
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
func (i *InterventionDockerJob) IsNil() bool                { return i == nil }

// --- Code Job Implementations ---

type CodeLogJob struct {
	ID          uuid.UUID
	Entity      ecs.Entity
	File        string
	Message     string
	Monitor     string
	Color       string
	Status      string
	Severity    string
	Summary     string
	Action      string
	NextSteps   string
	EnqueueTime time.Time
	StartTime   time.Time
}

func (c *CodeLogJob) Execute() Result {
	payload := map[string]interface{}{
		"type":     "code",
		"color":    c.Color,
		"severity": c.Severity,
		"status":   c.Status,
	}

	f, err := os.OpenFile(c.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
	}
	defer f.Close()

	now := time.Now().UTC()
	entry := struct {
		Timestamp string `json:"timestamp"`
		Type      string `json:"type"`
		Monitor   string `json:"monitor"`
		JobID     string `json:"job_id"`
		Color     string `json:"color"`
		Status    string `json:"status"`
		Severity  string `json:"severity"`
		Summary   string `json:"summary"`
		Action    string `json:"action"`
		NextSteps string `json:"next_steps,omitempty"`
		Message   string `json:"message,omitempty"`
	}{
		Timestamp: now.Format(time.RFC3339Nano),
		Type:      "code",
		Monitor:   c.Monitor,
		JobID:     c.ID.String(),
		Color:     c.Color,
		Status:    c.Status,
		Severity:  c.Severity,
		Summary:   c.Summary,
		Action:    c.Action,
		NextSteps: c.NextSteps,
		Message:   c.Message,
	}

	line, err := json.Marshal(entry)
	if err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: fmt.Errorf("failed to marshal log entry: %w", err), Payload: payload}
	}

	line = append(line, '\n')
	if _, err = f.Write(line); err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: fmt.Errorf("failed to write log entry: %w", err), Payload: payload}
	}

	return Result{ID: c.ID, Ent: c.Entity, Err: nil, Payload: payload}
}

func (c *CodeLogJob) Copy() Job                  { job := *c; return &job }
func (c *CodeLogJob) GetEnqueueTime() time.Time  { return c.EnqueueTime }
func (c *CodeLogJob) SetEnqueueTime(t time.Time) { c.EnqueueTime = t }
func (c *CodeLogJob) GetStartTime() time.Time    { return c.StartTime }
func (c *CodeLogJob) SetStartTime(t time.Time)   { c.StartTime = t }
func (c *CodeLogJob) IsNil() bool                { return c == nil }

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
func (c *CodePagerDutyJob) IsNil() bool                { return c == nil }

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
func (c *CodeSlackJob) IsNil() bool                { return c == nil }

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
func (c *CodeEmailJob) IsNil() bool                { return c == nil }

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
func (c *CodeWebhookJob) IsNil() bool                { return c == nil }
