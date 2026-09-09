package jobs

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"

	"cpra/internal/loader/schema"
)

// CodeDatadogJob posts an event to the Datadog events API. Pure stdlib
// (JSON POST with DD-API-KEY header), always compiled.
type CodeDatadogJob struct {
	Execution
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

func newCodeDatadogJob(cfg *schema.CodeNotificationDatadog, monitor, color, message string, entity ecs.Entity) (Job, error) {
	return &CodeDatadogJob{
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
		"title":      "CPRA alert: " + c.Monitor,
		"text":       c.Message,
		"alert_type": "error",
		"tags":       c.Tags,
	})
	if err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
	}
	url := fmt.Sprintf("https://api.%s/api/v1/events", site)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("DD-API-KEY", c.APIKey)
	if c.AppKey != "" {
		req.Header.Set("DD-APPLICATION-KEY", c.AppKey)
	}
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
