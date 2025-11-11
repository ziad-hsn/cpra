package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	ping "github.com/prometheus-community/pro-bing"

	"cpra/internal/loader/schema"
)

// Global ICMP pinger pool (Option B): host-keyed reusable pingers with bounded concurrency.
var (
	icmpPingerPool sync.Map                    // map[string]*pooledPinger
	icmpPingerSem  = make(chan struct{}, 2048) // limit concurrent ICMP executions
)

type pooledPinger struct {
	mu   sync.Mutex
	host string
	pr   *ping.Pinger
}

func getPooledPinger(host string) (*pooledPinger, error) {
	if v, ok := icmpPingerPool.Load(host); ok {
		return v.(*pooledPinger), nil
	}
	pr, err := ping.NewPinger(host)
	if err != nil {
		return nil, err
	}
	switch runtime.GOOS {
	case "linux":
		pr.SetPrivileged(false)
	default:
		pr.SetPrivileged(true)
	}
	pp := &pooledPinger{host: host, pr: pr}
	actual, _ := icmpPingerPool.LoadOrStore(host, pp)
	if actual != pp {
		pp = actual.(*pooledPinger)
	}
	return pp, nil
}

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
			URL:     cfg.Url,
			Method:  cfg.Method,
			Timeout: timeout,
			Retries: cfg.Retries,
			Client:  *GetHTTPClient(timeout),
			payload: map[string]interface{}{"type": "pulse", "driver": "http"},
		}, nil
	case *schema.PulseTCPConfig:
		return &PulseTCPJob{
			ID:      uuid.New(),
			Entity:  jobID,
			Host:    cfg.Host,
			Port:    cfg.Port,
			Timeout: timeout,
			Retries: cfg.Retries,
			payload: map[string]interface{}{"type": "pulse", "driver": "tcp"},
		}, nil
	case *schema.PulseICMPConfig:
		return &PulseICMPJob{
			ID:              uuid.New(),
			Entity:          jobID,
			Host:            cfg.Host,
			Timeout:         timeout,
			Count:           cfg.Count,
			Retries:         cfg.Retries,
			IgnorePrivilege: cfg.Privilege,
			payload:         map[string]interface{}{"type": "pulse", "driver": "icmp"},
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
			Container: interventionSchema.Target.(*schema.InterventionTargetDocker).Container,
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
	var b strings.Builder
	// Pre-size approximately to reduce reallocations
	b.Grow(len(tpl.Title) + len("\nMonitor: ") + len(monitor) +
		len("\nStatus: ") + len(tpl.Status) + len("\nSeverity: ") + len(tpl.Severity) +
		len("\nSummary: ") + len(tpl.Summary) + len("\nRecommended Action: ") + len(tpl.Action) +
		len("\nNext Steps: ") + len(tpl.NextSteps) + 8)
	b.WriteString(tpl.Title)
	b.WriteString("\nMonitor: ")
	b.WriteString(monitor)
	b.WriteString("\nStatus: ")
	b.WriteString(tpl.Status)
	b.WriteString("\nSeverity: ")
	b.WriteString(strings.ToUpper(tpl.Severity))
	b.WriteString("\nSummary: ")
	b.WriteString(tpl.Summary)
	b.WriteString("\nRecommended Action: ")
	b.WriteString(tpl.Action)
	b.WriteString("\nNext Steps: ")
	b.WriteString(tpl.NextSteps)
	return b.String()
}

// CreateCodeJob creates a new code alert job based on the provided configuration.
func CreateCodeJob(monitor string, config schema.CodeConfig, jobID ecs.Entity, color string) (Job, error) {
	template := codeAlertTemplateFor(color)
	colorClone := color
	monitorClone := monitor

	switch config.Notify {
	case "log":
		return &CodeLogJob{
			ID:        uuid.New(),
			File:      config.Config.(*schema.CodeNotificationLog).File,
			Entity:    jobID,
			Monitor:   monitorClone,
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
			Color:   colorClone,
		}, nil
	case "slack":
		return &CodeSlackJob{
			ID:      uuid.New(),
			Entity:  jobID,
			Monitor: monitorClone,
			Color:   colorClone,
		}, nil
	case "email":
		return &CodeEmailJob{
			ID:      uuid.New(),
			Entity:  jobID,
			Monitor: monitorClone,
			Color:   colorClone,
		}, nil
	case "webhook":
		return &CodeWebhookJob{
			ID:      uuid.New(),
			Entity:  jobID,
			Monitor: monitorClone,
			Color:   colorClone,
		}, nil
	default:
		return nil, fmt.Errorf("unknown code notification type: %s for job creation", config.Notify)
	}
}

// --- Pulse Job Implementations ---

type PulseHTTPJob struct {
	EnqueueTime time.Time
	StartTime   time.Time
	Client      http.Client
	URL         string
	Method      string
	Timeout     time.Duration
	Retries     int
	Entity      ecs.Entity
	ID          uuid.UUID
	payload     map[string]interface{}
}

func (p *PulseHTTPJob) Execute() Result {
	var lastErr error
	attempts := p.Retries + 1
	payload := p.payload

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
		_ = resp.Body.Close()
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
	EnqueueTime time.Time
	StartTime   time.Time
	Host        string
	Port        int
	Timeout     time.Duration
	Retries     int
	Entity      ecs.Entity
	ID          uuid.UUID
	payload     map[string]interface{}
}

func (p *PulseTCPJob) Execute() Result {
	payload := p.payload
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
	EnqueueTime     time.Time
	StartTime       time.Time
	Host            string
	Timeout         time.Duration
	Count           int
	Retries         int
	Entity          ecs.Entity
	ID              uuid.UUID
	IgnorePrivilege bool
	payload         map[string]interface{}
}

//var errICMPPrivilege = errors.New("icmp requires elevated privileges")

func (p *PulseICMPJob) Execute() Result {
	// Concurrency bound to avoid socket pressure
	icmpPingerSem <- struct{}{}
	defer func() { <-icmpPingerSem }()

	payload := p.payload
	// Reset per-execution dynamic fields
	delete(payload, "privilege_ignored")

	attempts := p.Retries + 1
	if attempts < 1 {
		attempts = 1
	}

	count := p.Count
	if count <= 0 {
		count = 1
	}
	payload["count"] = count

	// Get pooled pinger for host
	pp, err := getPooledPinger(p.Host)
	if err != nil {
		return Result{ID: p.ID, Ent: p.Entity, Err: err, Payload: payload}
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		pp.mu.Lock()
		pr := pp.pr
		pr.Count = count
		if p.Timeout > 0 {
			pr.Timeout = p.Timeout
		} else {
			pr.Timeout = time.Duration(count)*time.Second + 500*time.Millisecond
		}

		if err := pr.Run(); err == nil {
			stats := pr.Statistics()
			pp.mu.Unlock()
			if stats != nil && stats.PacketsRecv > 0 {
				return Result{ID: p.ID, Ent: p.Entity, Err: nil, Payload: payload}
			}
			lastErr = fmt.Errorf("no packets received")
		} else {
			// privilege fallback
			if !pr.Privileged() && isPrivilegeError(err) {
				pr.SetPrivileged(true)
				if err2 := pr.Run(); err2 == nil {
					stats := pr.Statistics()
					pp.mu.Unlock()
					if stats != nil && stats.PacketsRecv > 0 {
						return Result{ID: p.ID, Ent: p.Entity, Err: nil, Payload: payload}
					}
					lastErr = fmt.Errorf("no packets received")
				} else {
					pp.mu.Unlock()
					if p.IgnorePrivilege && isPrivilegeError(err2) {
						payload["privilege_ignored"] = true
						return Result{ID: p.ID, Ent: p.Entity, Err: nil, Payload: payload}
					}
					lastErr = err2
				}
			} else {
				pp.mu.Unlock()
				if p.IgnorePrivilege && isPrivilegeError(err) {
					payload["privilege_ignored"] = true
					return Result{ID: p.ID, Ent: p.Entity, Err: nil, Payload: payload}
				}
				lastErr = err
			}
		}

		if attempt < attempts-1 {
			time.Sleep(50 * time.Millisecond)
		}
	}

	return Result{
		ID:      p.ID,
		Ent:     p.Entity,
		Err:     fmt.Errorf("icmp check failed for %s after %d attempt(s): %w", p.Host, attempts, lastErr),
		Payload: payload,
	}
}

// isPrivilegeError checks common privilege-related error strings from pinger
func isPrivilegeError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
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
	EnqueueTime time.Time
	StartTime   time.Time
	Container   string
	Timeout     time.Duration
	Retries     int
	Entity      ecs.Entity
	ID          uuid.UUID
}

func (i *InterventionDockerJob) Execute() Result {
	payload := map[string]interface{}{"type": "intervention"}
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return Result{ID: i.ID, Ent: i.Entity, Err: fmt.Errorf("failed to create docker client: %w", err), Payload: payload}
	}
	defer func() { _ = cli.Close() }()

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
	EnqueueTime time.Time
	StartTime   time.Time
	Status      string
	Monitor     string
	Color       string
	Severity    string
	Summary     string
	Action      string
	NextSteps   string
	File        string
	Entity      ecs.Entity
	ID          uuid.UUID
}

func (c *CodeLogJob) Execute() Result {
	payload := map[string]interface{}{
		"type":     "code",
		"color":    c.Color,
		"severity": c.Severity,
		"status":   c.Status,
	}

	// Build message on-demand
	tpl := codeAlertTemplate{
		Title:     codeAlertTemplateFor(c.Color).Title,
		Status:    c.Status,
		Severity:  c.Severity,
		Summary:   c.Summary,
		Action:    c.Action,
		NextSteps: c.NextSteps,
	}
	message := buildCodeNotificationMessage(c.Monitor, tpl)

	f, err := os.OpenFile(c.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
	}
	defer func() { _ = f.Close() }()

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
		Message:   message,
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
	EnqueueTime time.Time
	StartTime   time.Time
	Monitor     string
	Message     string
	Color       string
	Entity      ecs.Entity
	ID          uuid.UUID
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
	EnqueueTime time.Time
	StartTime   time.Time
	Monitor     string
	Message     string
	Color       string
	Entity      ecs.Entity
	ID          uuid.UUID
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
	EnqueueTime time.Time
	StartTime   time.Time
	Monitor     string
	Message     string
	Color       string
	Entity      ecs.Entity
	ID          uuid.UUID
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
	EnqueueTime time.Time
	StartTime   time.Time
	Monitor     string
	Message     string
	Color       string
	Entity      ecs.Entity
	ID          uuid.UUID
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
