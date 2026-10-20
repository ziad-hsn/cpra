package jobs

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"

	"github.com/ziad-hsn/cpra/internal/manifest"
)

// CodeDatadogJob posts an event to the Datadog events API. Pure stdlib
// (JSON POST with DD-API-KEY header), always compiled.
type CodeDatadogJob struct {
	Execution
	URL         string
	EnqueueTime time.Time
	StartTime   time.Time
	Monitor     string
	Message     string
	Color       string
	APIKey      string
	AppKey      string
	Site        string
	Tags        []string
	Entity      ecs.Entity
	ID          uuid.UUID
}

func newCodeDatadogJob(cfg *manifest.CodeNotificationDatadog, monitor, color, message string, entity ecs.Entity) (Job, error) {
	if cfg.URL != "" {
		if err := validateTargetURL(cfg.URL); err != nil {
			return nil, err
		}
	}

	return &CodeDatadogJob{
		URL:     cfg.URL,
		ID:      uuid.New(),
		Entity:  entity,
		Monitor: monitor,
		Color:   color,
		Message: message,
		APIKey:  cfg.APIKey,
		AppKey:  cfg.AppKey,
		Site:    cfg.Site,
		Tags:    append([]string(nil), cfg.Tags...),
	}, nil
}

func (c *CodeDatadogJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(result.Err) }()
	ctx, cancel := operationContext(c.Context(), 10*time.Second)
	defer cancel()

	payload := map[string]interface{}{"type": "code", "driver": "datadog", "color": c.Color}
	site := c.Site
	if site == "" {
		site = "datadoghq.com"
	}
	body, err := json.Marshal(map[string]interface{}{
		"title":      "CPRa " + codeAlertTemplateFor(c.Color).Title + ": " + c.Monitor,
		"text":       c.notificationMessage(c.Message),
		"alert_type": datadogAlertType(c.Color),
		"tags":       c.Tags,
	})
	if err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
	}
	target, err := notificationURL(c.URL, fmt.Sprintf("https://api.%s/api/v1/events", site))
	if err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(string(body)))
	if err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("DD-API-KEY", c.APIKey)
	if c.AppKey != "" {
		req.Header.Set("DD-APPLICATION-KEY", c.AppKey)
	}
	resp, err := doEffect(effectHTTPClient(10*time.Second), req)
	if err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
	}
	defer func() { _ = resp.Body.Close() }()
	if err := notificationResponseError("datadog", resp); err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
	}
	return Result{ID: c.ID, Ent: c.Entity, Err: nil, Payload: payload}
}

func datadogAlertType(color string) string {
	switch color {
	case "red":
		return "error"
	case "yellow", "blue":
		return "warning"
	case "green":
		return "success"
	default:
		return "info"
	}
}

func (c *CodeDatadogJob) Copy() Job {
	job := *c
	job.Tags = append([]string(nil), c.Tags...)
	return &job
}
func (c *CodeDatadogJob) GetEnqueueTime() time.Time  { return c.EnqueueTime }
func (c *CodeDatadogJob) SetEnqueueTime(t time.Time) { c.EnqueueTime = t }
func (c *CodeDatadogJob) GetStartTime() time.Time    { return c.StartTime }
func (c *CodeDatadogJob) SetStartTime(t time.Time)   { c.StartTime = t }
func (c *CodeDatadogJob) IsNil() bool                { return c == nil }
