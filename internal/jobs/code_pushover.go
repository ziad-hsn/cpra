package jobs

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"

	"cpra/internal/loader/schema"
)

// CodePushoverJob delivers an alert to a Pushover user/group via the push
// notification API. Pure stdlib (form-encoded POST), always compiled.
type CodePushoverJob struct {
	Execution
	EnqueueTime time.Time
	StartTime   time.Time
	Monitor     string
	Message     string
	Color       string
	AppToken    string
	UserKey     string
	Title       string
	Priority    int
	Retry       int
	Expire      int
	Sound       string
	Entity      ecs.Entity
	ID          uuid.UUID
}

func newCodePushoverJob(cfg *schema.CodeNotificationPushover, monitor, color, message string, entity ecs.Entity) (Job, error) {
	if cfg.Priority < -2 || cfg.Priority > 2 {
		return nil, fmt.Errorf("pushover priority must be between -2 and 2")
	}
	retry, expire := cfg.Retry, cfg.Expire
	if cfg.Priority == 2 {
		if retry == 0 {
			retry = 60
		}
		if expire == 0 {
			expire = 1800
		}
		if retry < 30 || expire < 1 || expire > 10800 {
			return nil, fmt.Errorf("pushover emergency retry must be at least 30 seconds and expire between 1 and 10800 seconds")
		}
	}
	return &CodePushoverJob{
		ID:       uuid.New(),
		Entity:   entity,
		Monitor:  monitor,
		Color:    color,
		Message:  message,
		AppToken: cfg.AppToken,
		UserKey:  cfg.UserKey,
		Title:    cfg.Title,
		Priority: cfg.Priority,
		Retry:    retry,
		Expire:   expire,
		Sound:    cfg.Sound,
	}, nil
}

func (c *CodePushoverJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(result.Err) }()
	ctx, cancel := operationContext(c.Context(), 10*time.Second)
	defer cancel()

	payload := map[string]interface{}{"type": "code", "driver": "pushover", "color": c.Color}
	form := url.Values{}
	form.Set("token", c.AppToken)
	form.Set("user", c.UserKey)
	form.Set("message", c.Message)
	if c.Title != "" {
		form.Set("title", c.Title)
	}
	if c.Priority != 0 {
		form.Set("priority", strconv.Itoa(c.Priority))
	}
	if c.Priority == 2 {
		form.Set("retry", strconv.Itoa(c.Retry))
		form.Set("expire", strconv.Itoa(c.Expire))
	}
	if c.Sound != "" {
		form.Set("sound", c.Sound)
	}
	resp, err := postNotification(ctx, "https://api.pushover.net/1/messages.json", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
	}
	defer func() { _ = resp.Body.Close() }()
	if err := notificationStatusError(resp.StatusCode); err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
	}
	return Result{ID: c.ID, Ent: c.Entity, Err: nil, Payload: payload}
}

func (c *CodePushoverJob) Copy() Job                  { job := *c; return &job }
func (c *CodePushoverJob) GetEnqueueTime() time.Time  { return c.EnqueueTime }
func (c *CodePushoverJob) SetEnqueueTime(t time.Time) { c.EnqueueTime = t }
func (c *CodePushoverJob) GetStartTime() time.Time    { return c.StartTime }
func (c *CodePushoverJob) SetStartTime(t time.Time)   { c.StartTime = t }
func (c *CodePushoverJob) IsNil() bool                { return c == nil }
