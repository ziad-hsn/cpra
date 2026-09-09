package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	ping "github.com/prometheus-community/pro-bing"

	"cpra/internal/loader/schema"
)

var icmpPingerSem = make(chan struct{}, 2048)

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

// EnqueueTimeTracker is an optional interface implemented by jobs that do NOT
// track their enqueue time (e.g. immutable jobs whose SetEnqueueTime
// is a no-op). The queue checks for it to skip the per-dequeue GetEnqueueTime
// call in wait-time accounting.
type EnqueueTimeTracker interface {
	TracksEnqueueTime() bool
}

// CreatePulseJob creates a new pulse job based on the provided schema.
func CreatePulseJob(pulseSchema schema.Pulse, jobID ecs.Entity) (Job, error) {
	timeout := pulseSchema.Timeout
	switch cfg := pulseSchema.Config.(type) {
	case *schema.PulseHTTPConfig:
		method := cfg.Method
		if method == "" {
			method = http.MethodGet
		}
		return &PulseHTTPJob{
			ID:                 uuid.New(),
			Entity:             jobID,
			URL:                cfg.Url,
			Method:             method,
			Headers:            cfg.Headers,
			Body:               cfg.Body,
			ExpectedStatus:     cfg.ExpectedStatus,
			InsecureSkipVerify: cfg.InsecureSkipVerify,
			Timeout:            timeout,
			Retries:            cfg.Retries,
			Client:             *GetHTTPClient(timeout),
			payload:            map[string]interface{}{"type": "pulse", "driver": "http"},
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

	case *schema.PulseDNSConfig:
		return &PulseDNSJob{
			ID:      uuid.New(),
			Entity:  jobID,
			Host:    cfg.Host,
			Server:  cfg.Server,
			Timeout: timeout,
			Retries: cfg.Retries,
			payload: map[string]interface{}{"type": "pulse", "driver": "dns"},
		}, nil
	case *schema.PulseUDPConfig:
		return &PulseUDPJob{
			ID:          uuid.New(),
			Entity:      jobID,
			Host:        cfg.Host,
			Port:        cfg.Port,
			SendPayload: cfg.Payload,
			Timeout:     timeout,
			Retries:     cfg.Retries,
			payload:     map[string]interface{}{"type": "pulse", "driver": "udp"},
		}, nil
	case *schema.PulseGRPCConfig:
		return &PulseGRPCJob{
			ID:      uuid.New(),
			Entity:  jobID,
			Host:    cfg.Host,
			Port:    cfg.Port,
			Service: cfg.Service,
			Timeout: timeout,
			Retries: cfg.Retries,
			payload: map[string]interface{}{"type": "pulse", "driver": "grpc"},
		}, nil
	case *schema.PulseDockerConfig:
		return &PulseDockerJob{
			ID:        uuid.New(),
			Entity:    jobID,
			Container: cfg.Container,
			Timeout:   timeout,
			Retries:   cfg.Retries,
			payload:   map[string]interface{}{"type": "pulse", "driver": "docker"},
		}, nil
	case *schema.PulseRedisConfig:
		return newPulseRedisJob(cfg, timeout, jobID)
	case *schema.PulsePostgresConfig:
		return newPulsePostgresJob(cfg, timeout, jobID)
	case *schema.PulseMySQLConfig:
		return newPulseMySQLJob(cfg, timeout, jobID)
	case *schema.PulseMongoConfig:
		return newPulseMongoJob(cfg, timeout, jobID)
	case *schema.PulseRabbitMQConfig:
		return newPulseRabbitMQJob(cfg, timeout, jobID)
	case *schema.PulseKafkaConfig:
		return newPulseKafkaJob(cfg, timeout, jobID)
	case *schema.PulseTLSConfig:
		return newPulseTLSJob(cfg, timeout, jobID)
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
	case "kubernetes":
		t, ok := interventionSchema.Target.(*schema.InterventionTargetKubernetes)
		if !ok || t == nil {
			return nil, fmt.Errorf("intervention 'kubernetes' requires a kubernetes target")
		}
		return newInterventionKubernetesJob(t, retries, jobID)
	case "webhook":
		t, ok := interventionSchema.Target.(*schema.InterventionTargetWebhook)
		if !ok || t == nil {
			return nil, fmt.Errorf("intervention 'webhook' requires a webhook target")
		}
		return newInterventionWebhookJob(t, retries, jobID)
	case "systemd":
		t, ok := interventionSchema.Target.(*schema.InterventionTargetSystemd)
		if !ok || t == nil {
			return nil, fmt.Errorf("intervention 'systemd' requires a systemd target")
		}
		return newInterventionSystemdJob(t, retries, jobID)
	case "aws":
		t, ok := interventionSchema.Target.(*schema.InterventionTargetAWS)
		if !ok || t == nil {
			return nil, fmt.Errorf("intervention 'aws' requires an aws target")
		}
		return newInterventionAWSJob(t, retries, jobID)
	default:
		return nil, fmt.Errorf("unknown intervention action %q for job creation", interventionSchema.Action)
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
		cfg, ok := config.Config.(*schema.CodeNotificationLog)
		if !ok || cfg == nil {
			return nil, fmt.Errorf("code notification 'log' requires a log config")
		}
		return &CodeLogJob{
			ID:        uuid.New(),
			File:      cfg.File,
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
		cfg, ok := config.Config.(*schema.CodeNotificationPagerDuty)
		if !ok || cfg == nil || cfg.RoutingKey == "" {
			return nil, fmt.Errorf("code notification 'pagerduty' requires complete pagerduty configuration")
		}
		return &CodePagerDutyJob{ID: uuid.New(), Entity: jobID, Monitor: monitorClone, Color: colorClone,
			Message: buildCodeNotificationMessage(monitorClone, template), URL: cfg.URL, RoutingKey: cfg.RoutingKey}, nil

	case "slack":
		cfg, ok := config.Config.(*schema.CodeNotificationSlack)
		if !ok || cfg == nil || cfg.WebHook == "" {
			return nil, fmt.Errorf("code notification 'slack' requires complete slack configuration")
		}
		return &CodeSlackJob{ID: uuid.New(), Entity: jobID, Monitor: monitorClone, Color: colorClone,
			Message: buildCodeNotificationMessage(monitorClone, template), WebhookURL: cfg.WebHook}, nil

	case "email":
		cfg, ok := config.Config.(*schema.CodeNotificationEmail)
		if !ok || cfg == nil || cfg.Server == "" || cfg.From == "" || cfg.To == "" {
			return nil, fmt.Errorf("code notification 'email' requires complete email configuration")
		}
		return &CodeEmailJob{ID: uuid.New(), Entity: jobID, Monitor: monitorClone, Color: colorClone,
			Message: buildCodeNotificationMessage(monitorClone, template), Config: *cfg}, nil

	case "webhook":
		cfg, ok := config.Config.(*schema.CodeNotificationWebhook)
		if !ok || cfg == nil || cfg.URL == "" {
			return nil, fmt.Errorf("code notification 'webhook' requires complete webhook configuration")
		}
		return &CodeWebhookJob{ID: uuid.New(), Entity: jobID, Monitor: monitorClone, Color: colorClone,
			Message: buildCodeNotificationMessage(monitorClone, template), URL: cfg.URL, Method: cfg.Method, Headers: cfg.Headers}, nil

	case "telegram":
		cfg, ok := config.Config.(*schema.CodeNotificationTelegram)
		if !ok || cfg == nil {
			return nil, fmt.Errorf("code notification 'telegram' requires a telegram config")
		}
		return &CodeTelegramJob{
			ID:       uuid.New(),
			Entity:   jobID,
			Monitor:  monitorClone,
			Color:    colorClone,
			Message:  buildCodeNotificationMessage(monitorClone, template),
			BotToken: cfg.BotToken,
			ChatID:   cfg.ChatID,
		}, nil
	case "discord":
		cfg, ok := config.Config.(*schema.CodeNotificationDiscord)
		if !ok || cfg == nil {
			return nil, fmt.Errorf("code notification 'discord' requires a discord config")
		}
		return &CodeDiscordJob{
			ID:         uuid.New(),
			Entity:     jobID,
			Monitor:    monitorClone,
			Color:      colorClone,
			Message:    buildCodeNotificationMessage(monitorClone, template),
			WebhookURL: cfg.WebhookURL,
		}, nil
	case "opsgenie":
		cfg, ok := config.Config.(*schema.CodeNotificationOpsgenie)
		if !ok || cfg == nil {
			return nil, fmt.Errorf("code notification 'opsgenie' requires an opsgenie config")
		}
		return &CodeOpsgenieJob{
			ID:      uuid.New(),
			Entity:  jobID,
			Monitor: monitorClone,
			Color:   colorClone,
			Message: buildCodeNotificationMessage(monitorClone, template),
			APIKey:  cfg.APIKey,
			URL:     cfg.URL,
		}, nil
	case "mattermost":
		cfg, ok := config.Config.(*schema.CodeNotificationMattermost)
		if !ok || cfg == nil {
			return nil, fmt.Errorf("code notification 'mattermost' requires a mattermost config")
		}
		return &CodeMattermostJob{
			ID:         uuid.New(),
			Entity:     jobID,
			Monitor:    monitorClone,
			Color:      colorClone,
			Message:    buildCodeNotificationMessage(monitorClone, template),
			WebhookURL: cfg.WebhookURL,
			Channel:    cfg.Channel,
			Username:   cfg.Username,
		}, nil
	case "victorops":
		cfg, ok := config.Config.(*schema.CodeNotificationVictorOps)
		if !ok || cfg == nil {
			return nil, fmt.Errorf("code notification 'victorops' requires a victorops config")
		}
		return &CodeVictorOpsJob{
			ID:              uuid.New(),
			Entity:          jobID,
			Monitor:         monitorClone,
			Color:           colorClone,
			Message:         buildCodeNotificationMessage(monitorClone, template),
			RestEndpointKey: cfg.RestEndpointKey,
			RoutingKey:      cfg.RoutingKey,
			MessageType:     cfg.MessageType,
			EntityID:        cfg.EntityID,
		}, nil
	case "teams":
		cfg, ok := config.Config.(*schema.CodeNotificationTeams)
		if !ok || cfg == nil {
			return nil, fmt.Errorf("code notification 'teams' requires a teams config")
		}
		return newCodeTeamsJob(cfg, monitorClone, colorClone, buildCodeNotificationMessage(monitorClone, template), jobID)
	case "pushover":
		cfg, ok := config.Config.(*schema.CodeNotificationPushover)
		if !ok || cfg == nil {
			return nil, fmt.Errorf("code notification 'pushover' requires a pushover config")
		}
		return newCodePushoverJob(cfg, monitorClone, colorClone, buildCodeNotificationMessage(monitorClone, template), jobID)
	case "twilio":
		cfg, ok := config.Config.(*schema.CodeNotificationTwilio)
		if !ok || cfg == nil {
			return nil, fmt.Errorf("code notification 'twilio' requires a twilio config")
		}
		return newCodeTwilioJob(cfg, monitorClone, colorClone, buildCodeNotificationMessage(monitorClone, template), jobID)
	case "datadog":
		cfg, ok := config.Config.(*schema.CodeNotificationDatadog)
		if !ok || cfg == nil {
			return nil, fmt.Errorf("code notification 'datadog' requires a datadog config")
		}
		return newCodeDatadogJob(cfg, monitorClone, colorClone, buildCodeNotificationMessage(monitorClone, template), jobID)
	default:
		return nil, fmt.Errorf("unknown code notification type: %s for job creation", config.Notify)
	}
}

// CreateCodeJobs creates one or more code alert jobs for a color. When the
// config references a notification group, it resolves the group to its
// endpoints and creates one job per endpoint; otherwise it creates a single
// inline job.
func CreateCodeJobs(monitor string, config schema.CodeConfig, jobID ecs.Entity, color string, endpoints map[string]schema.Endpoint, groups schema.NotificationGroups) ([]Job, error) {
	if config.NotifyGroup == "" {
		job, err := CreateCodeJob(monitor, config, jobID, color)
		if err != nil {
			return nil, err
		}
		return []Job{job}, nil
	}

	names, ok := groups[config.NotifyGroup]
	if !ok {
		return nil, fmt.Errorf("notification group %q not found", config.NotifyGroup)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("notification group %q has no endpoints", config.NotifyGroup)
	}

	jobs := make([]Job, 0, len(names))
	for _, name := range names {
		ep, ok := endpoints[name]
		if !ok {
			return nil, fmt.Errorf("endpoint %q not found for group %q", name, config.NotifyGroup)
		}
		inline := schema.CodeConfig{
			Dispatch: config.Dispatch,
			Notify:   ep.Type,
			Config:   ep.Config,
		}
		job, err := CreateCodeJob(monitor, inline, jobID, color)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

// --- Pulse Job Implementations ---

type PulseHTTPJob struct {
	Execution
	EnqueueTime        time.Time
	StartTime          time.Time
	Client             http.Client
	URL                string
	Method             string
	Headers            map[string]string
	Body               string
	ExpectedStatus     []int
	InsecureSkipVerify bool
	Timeout            time.Duration
	Retries            int
	Entity             ecs.Entity
	ID                 uuid.UUID
	payload            map[string]interface{}
}

func (p *PulseHTTPJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(result.Err) }()
	ctx, cancel := operationContext(p.Context(), p.Timeout)
	defer cancel()

	if err := validateTargetURL(p.URL); err != nil {
		return Result{ID: p.ID, Ent: p.Entity, Err: err, Payload: p.payload}
	}
	var lastErr error
	attempts := p.Retries + 1
	if attempts < 1 {
		attempts = 1
	}
	payload := p.payload

	client := *httpClient(p.Timeout, p.InsecureSkipVerify)
	if p.Client.Transport != nil && !SSRFProtect && !p.InsecureSkipVerify {
		client.Transport = p.Client.Transport
	}
	if p.Method != "GET" && p.Method != "HEAD" && p.Method != "OPTIONS" {
		attempts = 1
	}

	for i := 0; i < attempts; i++ {
		var body io.Reader
		if p.Body != "" {
			body = strings.NewReader(p.Body)
		}
		req, err := http.NewRequestWithContext(ctx, p.Method, p.URL, body)
		if err != nil {
			return Result{ID: p.ID, Ent: p.Entity, Err: fmt.Errorf("failed to create http request: %w", err), Payload: payload}
		}
		for k, v := range p.Headers {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			if i < attempts-1 {
				if !retryDelay(ctx) {
					break
				}
			}
			continue
		}
		// Close response body immediately after checking status
		statusOk := p.statusOK(resp.StatusCode)
		_ = resp.Body.Close()
		if statusOk {
			return Result{ID: p.ID, Ent: p.Entity, Err: nil, Payload: payload}
		}
		lastErr = fmt.Errorf("received unexpected status code: %d", resp.StatusCode)
		if resp.StatusCode < 500 && resp.StatusCode != 429 {
			break
		}
	}
	return Result{ID: p.ID, Ent: p.Entity, Err: fmt.Errorf("http check failed after %d attempt(s): %w", attempts, lastErr), Payload: payload}
}

// statusOK reports whether a response status code is considered healthy. When
// ExpectedStatus is set, only those codes pass; otherwise any 2xx passes.
func (p *PulseHTTPJob) statusOK(code int) bool {
	if len(p.ExpectedStatus) > 0 {
		for _, s := range p.ExpectedStatus {
			if code == s {
				return true
			}
		}
		return false
	}
	return code >= 200 && code < 300
}

func (p *PulseHTTPJob) Copy() Job                  { job := *p; return &job }
func (p *PulseHTTPJob) GetEnqueueTime() time.Time  { return p.EnqueueTime }
func (p *PulseHTTPJob) SetEnqueueTime(t time.Time) { p.EnqueueTime = t }
func (p *PulseHTTPJob) GetStartTime() time.Time    { return p.StartTime }
func (p *PulseHTTPJob) SetStartTime(t time.Time)   { p.StartTime = t }
func (p *PulseHTTPJob) IsNil() bool                { return p == nil }

// PulseTCPJob implements a TCP pulse job.
type PulseTCPJob struct {
	Execution
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

func (p *PulseTCPJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(result.Err) }()
	ctx, cancel := operationContext(p.Context(), p.Timeout)
	defer cancel()

	payload := p.payload
	attempts := p.Retries + 1
	if attempts < 1 {
		attempts = 1
	}

	address := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))
	var lastErr error

	for attempt := 0; attempt < attempts; attempt++ {
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
		if err == nil {
			_ = conn.SetDeadline(time.Now().Add(p.Timeout))
			_ = conn.Close()
			return Result{ID: p.ID, Ent: p.Entity, Err: nil, Payload: payload}
		}
		lastErr = err
		if attempt < attempts-1 {
			if !retryDelay(ctx) {
				break
			}
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

// PulseICMPJob implements an ICMP pulse job.
type PulseICMPJob struct {
	Execution
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

func (p *PulseICMPJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(result.Err) }()
	ctx, cancel := operationContext(p.Context(), p.Timeout)
	defer cancel()
	result = Result{ID: p.ID, Ent: p.Entity, Payload: p.payload}
	select {
	case icmpPingerSem <- struct{}{}:
		defer func() { <-icmpPingerSem }()
	case <-ctx.Done():
		result.Err = ctx.Err()
		return
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, p.Host)
	if err != nil {
		result.Err = err
		return
	}
	if len(ips) == 0 {
		result.Err = fmt.Errorf("ICMP destination has no addresses")
		return
	}
	for n := 0; n <= max(0, p.Retries); n++ {
		pr, err := ping.NewPinger(ips[0].IP.String())
		if err != nil {
			result.Err = err
			return
		}
		pr.Count = p.Count
		if pr.Count <= 0 {
			pr.Count = 1
		}
		pr.Timeout = remaining(ctx)
		pr.SetPrivileged(runtime.GOOS != "linux")
		err = pr.RunWithContext(ctx)
		if err == nil && pr.Statistics().PacketsRecv > 0 {
			result.Err = nil
			return
		}
		if err == nil {
			err = fmt.Errorf("no ICMP replies received")
		}
		result.Err = err
		if n < p.Retries && !retryDelay(ctx) {
			break
		}
	}
	return
}

func (p *PulseICMPJob) Copy() Job                  { job := *p; return &job }
func (p *PulseICMPJob) GetEnqueueTime() time.Time  { return p.EnqueueTime }
func (p *PulseICMPJob) SetEnqueueTime(t time.Time) { p.EnqueueTime = t }
func (p *PulseICMPJob) GetStartTime() time.Time    { return p.StartTime }
func (p *PulseICMPJob) SetStartTime(t time.Time)   { p.StartTime = t }
func (p *PulseICMPJob) IsNil() bool                { return p == nil }

// PulseDNSJob checks that a hostname resolves via DNS.
type PulseDNSJob struct {
	Execution
	EnqueueTime time.Time
	StartTime   time.Time
	Host        string
	Server      string
	Timeout     time.Duration
	Retries     int
	Entity      ecs.Entity
	ID          uuid.UUID
	payload     map[string]interface{}
}

func (p *PulseDNSJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(result.Err) }()
	ctx, cancel := operationContext(p.Context(), p.Timeout)
	defer cancel()

	payload := p.payload
	attempts := p.Retries + 1
	if attempts < 1 {
		attempts = 1
	}

	resolver := &net.Resolver{}
	if p.Server != "" {
		resolver = &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				d := net.Dialer{Timeout: p.Timeout}
				return d.DialContext(ctx, network, net.JoinHostPort(p.Server, "53"))
			},
		}
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		_, err := resolver.LookupHost(ctx, p.Host)
		if err == nil {
			return Result{ID: p.ID, Ent: p.Entity, Err: nil, Payload: payload}
		}
		lastErr = err
		if attempt < attempts-1 {
			if !retryDelay(ctx) {
				break
			}
		}
	}
	return Result{ID: p.ID, Ent: p.Entity, Err: fmt.Errorf("dns check failed for %s after %d attempt(s): %w", p.Host, attempts, lastErr), Payload: payload}
}

func (p *PulseDNSJob) Copy() Job                  { job := *p; return &job }
func (p *PulseDNSJob) GetEnqueueTime() time.Time  { return p.EnqueueTime }
func (p *PulseDNSJob) SetEnqueueTime(t time.Time) { p.EnqueueTime = t }
func (p *PulseDNSJob) GetStartTime() time.Time    { return p.StartTime }
func (p *PulseDNSJob) SetStartTime(t time.Time)   { p.StartTime = t }
func (p *PulseDNSJob) IsNil() bool                { return p == nil }

// PulseUDPJob checks a UDP endpoint by sending a datagram and awaiting a reply.
type PulseUDPJob struct {
	Execution
	EnqueueTime time.Time
	StartTime   time.Time
	Host        string
	Port        int
	SendPayload string
	Timeout     time.Duration
	Retries     int
	Entity      ecs.Entity
	ID          uuid.UUID
	payload     map[string]interface{}
}

func (p *PulseUDPJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(result.Err) }()
	ctx, cancel := operationContext(p.Context(), p.Timeout)
	defer cancel()

	payload := p.payload
	attempts := p.Retries + 1
	if attempts < 1 {
		attempts = 1
	}
	address := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		conn, err := dialBounded(ctx, "udp", address)
		if err == nil {
			// dialBounded installed the operation deadline.
			if p.SendPayload == "" {
				err = fmt.Errorf("UDP health checks require a request payload and a reply")
			} else {
				if _, err = conn.Write([]byte(p.SendPayload)); err == nil {
					buf := make([]byte, 1024)
					var n int
					n, err = conn.Read(buf)
					if err == nil && n == 0 {
						err = fmt.Errorf("empty UDP response")
					}
				}
			}
			_ = conn.Close()
			if err == nil {
				return Result{ID: p.ID, Ent: p.Entity, Payload: payload}
			}
		}
		lastErr = err
		if attempt < attempts-1 {
			if !retryDelay(ctx) {
				break
			}
		}
	}
	return Result{ID: p.ID, Ent: p.Entity, Err: fmt.Errorf("udp check failed for %s after %d attempt(s): %w", address, attempts, lastErr), Payload: payload}
}

func (p *PulseUDPJob) Copy() Job                  { job := *p; return &job }
func (p *PulseUDPJob) GetEnqueueTime() time.Time  { return p.EnqueueTime }
func (p *PulseUDPJob) SetEnqueueTime(t time.Time) { p.EnqueueTime = t }
func (p *PulseUDPJob) GetStartTime() time.Time    { return p.StartTime }
func (p *PulseUDPJob) SetStartTime(t time.Time)   { p.StartTime = t }
func (p *PulseUDPJob) IsNil() bool                { return p == nil }

// PulseGRPCJob checks a gRPC endpoint's reachability via a TCP connect. A full
// gRPC health check would require the grpc-go client; this verifies the
// endpoint is listening.
type PulseGRPCJob struct {
	Execution
	EnqueueTime time.Time
	StartTime   time.Time
	Host        string
	Port        int
	Service     string
	Timeout     time.Duration
	Retries     int
	Entity      ecs.Entity
	ID          uuid.UUID
	payload     map[string]interface{}
}

func (p *PulseGRPCJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(result.Err) }()
	ctx, cancel := operationContext(p.Context(), p.Timeout)
	defer cancel()

	payload := p.payload
	attempts := p.Retries + 1
	if attempts < 1 {
		attempts = 1
	}
	address := net.JoinHostPort(p.Host, strconv.Itoa(p.Port))

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
		if err == nil {
			_ = conn.Close()
			return Result{ID: p.ID, Ent: p.Entity, Err: nil, Payload: payload}
		}
		lastErr = err
		if attempt < attempts-1 {
			if !retryDelay(ctx) {
				break
			}
		}
	}
	return Result{ID: p.ID, Ent: p.Entity, Err: fmt.Errorf("grpc check failed for %s after %d attempt(s): %w", address, attempts, lastErr), Payload: payload}
}

func (p *PulseGRPCJob) Copy() Job                  { job := *p; return &job }
func (p *PulseGRPCJob) GetEnqueueTime() time.Time  { return p.EnqueueTime }
func (p *PulseGRPCJob) SetEnqueueTime(t time.Time) { p.EnqueueTime = t }
func (p *PulseGRPCJob) GetStartTime() time.Time    { return p.StartTime }
func (p *PulseGRPCJob) SetStartTime(t time.Time)   { p.StartTime = t }
func (p *PulseGRPCJob) IsNil() bool                { return p == nil }

// PulseDockerJob checks that a Docker container is running.
type PulseDockerJob struct {
	Execution
	EnqueueTime time.Time
	StartTime   time.Time
	Container   string
	Timeout     time.Duration
	Retries     int
	Entity      ecs.Entity
	ID          uuid.UUID
	payload     map[string]interface{}
}

func (p *PulseDockerJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(result.Err) }()
	ctx, cancel := operationContext(p.Context(), p.Timeout)
	defer cancel()

	payload := p.payload
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return Result{ID: p.ID, Ent: p.Entity, Err: fmt.Errorf("failed to create docker client: %w", err), Payload: payload}
	}
	defer func() { _ = cli.Close() }()

	attempts := p.Retries + 1
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		inspect, err := cli.ContainerInspect(ctx, p.Container)
		if err == nil && inspect.State != nil && inspect.State.Running {
			return Result{ID: p.ID, Ent: p.Entity, Err: nil, Payload: payload}
		}
		if err == nil {
			lastErr = fmt.Errorf("container %q is not running", p.Container)
		} else {
			lastErr = err
		}
		if attempt < attempts-1 {
			if !retryDelay(ctx) {
				break
			}
		}
	}
	return Result{ID: p.ID, Ent: p.Entity, Err: fmt.Errorf("docker check failed for %q after %d attempt(s): %w", p.Container, attempts, lastErr), Payload: payload}
}

func (p *PulseDockerJob) Copy() Job                  { job := *p; return &job }
func (p *PulseDockerJob) GetEnqueueTime() time.Time  { return p.EnqueueTime }
func (p *PulseDockerJob) SetEnqueueTime(t time.Time) { p.EnqueueTime = t }
func (p *PulseDockerJob) GetStartTime() time.Time    { return p.StartTime }
func (p *PulseDockerJob) SetStartTime(t time.Time)   { p.StartTime = t }
func (p *PulseDockerJob) IsNil() bool                { return p == nil }

// --- Intervention Job Implementations ---

type InterventionDockerJob struct {
	Execution
	EnqueueTime time.Time
	StartTime   time.Time
	Container   string
	Timeout     time.Duration
	Retries     int
	Entity      ecs.Entity
	ID          uuid.UUID
}

func (i *InterventionDockerJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(result.Err) }()
	ctx, cancel := operationContext(i.Context(), i.Timeout)
	defer cancel()

	payload := map[string]interface{}{"type": "intervention"}
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return Result{ID: i.ID, Ent: i.Entity, Err: fmt.Errorf("failed to create docker client: %w", err), Payload: payload}
	}
	defer func() { _ = cli.Close() }()

	var lastErr error
	attempts := 1
	for attempt := 0; attempt < attempts; attempt++ {
		restartOptions := container.StopOptions{}
		if i.Timeout > 0 {
			// Docker interprets zero as an immediate kill. Preserve its default
			// grace when omitted and round positive subsecond values up.
			timeout := int(i.Timeout / time.Second)
			if i.Timeout%time.Second != 0 {
				timeout++
			}
			restartOptions.Timeout = &timeout
		}
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
func (i *InterventionDockerJob) IsNil() bool                { return i == nil }

// --- Code Job Implementations ---

type CodeLogJob struct {
	Execution
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

func (c *CodeLogJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(result.Err) }()
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

	// Ensure the parent directory exists so a configured path whose
	// directory is missing (e.g. /var/log/cpra) does not fail the write.
	if dir := filepath.Dir(c.File); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return Result{ID: c.ID, Ent: c.Entity, Err: fmt.Errorf("failed to create log directory %q: %w", dir, err), Payload: payload}
		}
	}
	f, err := os.OpenFile(c.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
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

// CodePagerDutyJob implements a PagerDuty notification job.
type CodePagerDutyJob struct {
	URL, RoutingKey string
	Execution
	EnqueueTime time.Time
	StartTime   time.Time
	Monitor     string
	Message     string
	Color       string
	Entity      ecs.Entity
	ID          uuid.UUID
}

func (c *CodePagerDutyJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(result.Err) }()
	ctx, cancel := operationContext(c.Context(), 10*time.Second)
	defer cancel()
	result = Result{ID: c.ID, Ent: c.Entity, Payload: map[string]interface{}{"type": "code", "driver": "pagerduty", "color": c.Color}}
	target := c.URL
	if target == "" {
		target = "https://events.pagerduty.com/v2/enqueue"
	}
	if c.RoutingKey == "" {
		result.Err = fmt.Errorf("pagerduty routing_key is required")
		return
	}
	action := "trigger"
	if c.Color == "green" {
		action = "resolve"
	}
	data := map[string]interface{}{"routing_key": c.RoutingKey, "event_action": action, "dedup_key": "cpra:" + c.Monitor,
		"payload": map[string]string{"summary": c.Message, "source": c.Monitor, "severity": "error"}}
	result.Err = sendJSON(ctx, http.MethodPost, target, nil, data)
	return
}

func (c *CodePagerDutyJob) Copy() Job                  { job := *c; return &job }
func (c *CodePagerDutyJob) GetEnqueueTime() time.Time  { return c.EnqueueTime }
func (c *CodePagerDutyJob) SetEnqueueTime(t time.Time) { c.EnqueueTime = t }
func (c *CodePagerDutyJob) GetStartTime() time.Time    { return c.StartTime }
func (c *CodePagerDutyJob) SetStartTime(t time.Time)   { c.StartTime = t }
func (c *CodePagerDutyJob) IsNil() bool                { return c == nil }

// CodeSlackJob implements a Slack notification job.
type CodeSlackJob struct {
	WebhookURL string
	Execution
	EnqueueTime time.Time
	StartTime   time.Time
	Monitor     string
	Message     string
	Color       string
	Entity      ecs.Entity
	ID          uuid.UUID
}

func (c *CodeSlackJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(result.Err) }()
	ctx, cancel := operationContext(c.Context(), 10*time.Second)
	defer cancel()
	result = Result{ID: c.ID, Ent: c.Entity, Payload: map[string]interface{}{"type": "code", "driver": "slack", "color": c.Color}}
	result.Err = sendJSON(ctx, http.MethodPost, c.WebhookURL, nil, map[string]string{"text": c.Message})
	return
}

func (c *CodeSlackJob) Copy() Job                  { job := *c; return &job }
func (c *CodeSlackJob) GetEnqueueTime() time.Time  { return c.EnqueueTime }
func (c *CodeSlackJob) SetEnqueueTime(t time.Time) { c.EnqueueTime = t }
func (c *CodeSlackJob) GetStartTime() time.Time    { return c.StartTime }
func (c *CodeSlackJob) SetStartTime(t time.Time)   { c.StartTime = t }
func (c *CodeSlackJob) IsNil() bool                { return c == nil }

// CodeEmailJob implements an email notification job.
type CodeEmailJob struct {
	Config schema.CodeNotificationEmail
	Execution
	EnqueueTime time.Time
	StartTime   time.Time
	Monitor     string
	Message     string
	Color       string
	Entity      ecs.Entity
	ID          uuid.UUID
}

func (c *CodeEmailJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(result.Err) }()
	ctx, cancel := operationContext(c.Context(), 10*time.Second)
	defer cancel()
	result = Result{ID: c.ID, Ent: c.Entity, Payload: map[string]interface{}{"type": "code", "driver": "email", "color": c.Color}}
	result.Err = sendEmail(ctx, c.Config, c.Message)
	return
}

func (c *CodeEmailJob) Copy() Job                  { job := *c; return &job }
func (c *CodeEmailJob) GetEnqueueTime() time.Time  { return c.EnqueueTime }
func (c *CodeEmailJob) SetEnqueueTime(t time.Time) { c.EnqueueTime = t }
func (c *CodeEmailJob) GetStartTime() time.Time    { return c.StartTime }
func (c *CodeEmailJob) SetStartTime(t time.Time)   { c.StartTime = t }
func (c *CodeEmailJob) IsNil() bool                { return c == nil }

// CodeWebhookJob implements a webhook notification job.
type CodeWebhookJob struct {
	URL, Method string
	Headers     map[string]string
	Execution
	EnqueueTime time.Time
	StartTime   time.Time
	Monitor     string
	Message     string
	Color       string
	Entity      ecs.Entity
	ID          uuid.UUID
}

func (c *CodeWebhookJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(result.Err) }()
	ctx, cancel := operationContext(c.Context(), 10*time.Second)
	defer cancel()
	result = Result{ID: c.ID, Ent: c.Entity, Payload: map[string]interface{}{"type": "code", "driver": "webhook", "color": c.Color}}
	method := c.Method
	if method == "" {
		method = http.MethodPost
	}
	result.Err = sendJSON(ctx, method, c.URL, c.Headers, map[string]string{"monitor": c.Monitor, "color": c.Color, "message": c.Message})
	return
}

func (c *CodeWebhookJob) Copy() Job                  { job := *c; return &job }
func (c *CodeWebhookJob) GetEnqueueTime() time.Time  { return c.EnqueueTime }
func (c *CodeWebhookJob) SetEnqueueTime(t time.Time) { c.EnqueueTime = t }
func (c *CodeWebhookJob) GetStartTime() time.Time    { return c.StartTime }
func (c *CodeWebhookJob) SetStartTime(t time.Time)   { c.StartTime = t }
func (c *CodeWebhookJob) IsNil() bool                { return c == nil }

// CodeTelegramJob delivers an alert to a Telegram chat via the Bot API.
type CodeTelegramJob struct {
	Execution
	EnqueueTime time.Time
	StartTime   time.Time
	Monitor     string
	Message     string
	Color       string
	BotToken    string
	ChatID      string
	Entity      ecs.Entity
	ID          uuid.UUID
}

func (c *CodeTelegramJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(result.Err) }()
	ctx, cancel := operationContext(c.Context(), 10*time.Second)
	defer cancel()

	payload := map[string]interface{}{"type": "code", "driver": "telegram", "color": c.Color}
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", c.BotToken)
	body, err := json.Marshal(map[string]string{"chat_id": c.ChatID, "text": c.Message})
	if err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
	}
	resp, err := postNotification(ctx, url, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
	}
	defer func() { _ = resp.Body.Close() }()
	if err := notificationStatusError(resp.StatusCode); err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
	}
	return Result{ID: c.ID, Ent: c.Entity, Err: nil, Payload: payload}
}

func (c *CodeTelegramJob) Copy() Job                  { job := *c; return &job }
func (c *CodeTelegramJob) GetEnqueueTime() time.Time  { return c.EnqueueTime }
func (c *CodeTelegramJob) SetEnqueueTime(t time.Time) { c.EnqueueTime = t }
func (c *CodeTelegramJob) GetStartTime() time.Time    { return c.StartTime }
func (c *CodeTelegramJob) SetStartTime(t time.Time)   { c.StartTime = t }
func (c *CodeTelegramJob) IsNil() bool                { return c == nil }

// CodeDiscordJob delivers an alert to a Discord channel via a webhook.
type CodeDiscordJob struct {
	Execution
	EnqueueTime time.Time
	StartTime   time.Time
	Monitor     string
	Message     string
	Color       string
	WebhookURL  string
	Entity      ecs.Entity
	ID          uuid.UUID
}

func (c *CodeDiscordJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(result.Err) }()
	ctx, cancel := operationContext(c.Context(), 10*time.Second)
	defer cancel()

	payload := map[string]interface{}{"type": "code", "driver": "discord", "color": c.Color}
	body, err := json.Marshal(map[string]string{"content": c.Message})
	if err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
	}
	resp, err := postNotification(ctx, c.WebhookURL, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
	}
	defer func() { _ = resp.Body.Close() }()
	if err := notificationStatusError(resp.StatusCode); err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
	}
	return Result{ID: c.ID, Ent: c.Entity, Err: nil, Payload: payload}
}

func (c *CodeDiscordJob) Copy() Job                  { job := *c; return &job }
func (c *CodeDiscordJob) GetEnqueueTime() time.Time  { return c.EnqueueTime }
func (c *CodeDiscordJob) SetEnqueueTime(t time.Time) { c.EnqueueTime = t }
func (c *CodeDiscordJob) GetStartTime() time.Time    { return c.StartTime }
func (c *CodeDiscordJob) SetStartTime(t time.Time)   { c.StartTime = t }
func (c *CodeDiscordJob) IsNil() bool                { return c == nil }

// CodeOpsgenieJob delivers an alert to Opsgenie via the v2 alerts API.
type CodeOpsgenieJob struct {
	Execution
	EnqueueTime time.Time
	StartTime   time.Time
	Monitor     string
	Message     string
	Color       string
	APIKey      string
	URL         string
	Entity      ecs.Entity
	ID          uuid.UUID
}

func (c *CodeOpsgenieJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(result.Err) }()
	ctx, cancel := operationContext(c.Context(), 10*time.Second)
	defer cancel()

	payload := map[string]interface{}{"type": "code", "driver": "opsgenie", "color": c.Color}
	url := c.URL
	if url == "" {
		url = "https://api.opsgenie.com/v2/alerts"
	}
	body, err := json.Marshal(map[string]string{
		"message":     truncateNotification(c.Message, 130),
		"description": truncateNotification(c.Message, 15000),
		"alias":       c.ID.String(),
	})
	if err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
	}
	req.Header.Set("Authorization", "GenieKey "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := GetHTTPClient(10 * time.Second).Do(req)
	if err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
	}
	defer func() { _ = resp.Body.Close() }()
	if err := notificationStatusError(resp.StatusCode); err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
	}
	return Result{ID: c.ID, Ent: c.Entity, Err: nil, Payload: payload}
}

func (c *CodeOpsgenieJob) Copy() Job                  { job := *c; return &job }
func (c *CodeOpsgenieJob) GetEnqueueTime() time.Time  { return c.EnqueueTime }
func (c *CodeOpsgenieJob) SetEnqueueTime(t time.Time) { c.EnqueueTime = t }
func (c *CodeOpsgenieJob) GetStartTime() time.Time    { return c.StartTime }
func (c *CodeOpsgenieJob) SetStartTime(t time.Time)   { c.StartTime = t }
func (c *CodeOpsgenieJob) IsNil() bool                { return c == nil }
