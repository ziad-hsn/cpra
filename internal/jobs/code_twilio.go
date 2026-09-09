//go:build twilio

package jobs

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"

	"cpra/internal/loader/schema"
)

// CodeTwilioJob delivers an alert as SMS via the Twilio API.
type CodeTwilioJob struct {
	Execution
	EnqueueTime time.Time
	StartTime   time.Time
	Monitor     string
	Message     string
	Color       string
	AccountSID  string
	AuthToken   string
	From        string
	To          string
	Entity      ecs.Entity
	ID          uuid.UUID
}

func newCodeTwilioJob(cfg *schema.CodeNotificationTwilio, monitor, color, message string, entity ecs.Entity) (Job, error) {
	return &CodeTwilioJob{
		ID:         uuid.New(),
		Entity:     entity,
		Monitor:    monitor,
		Color:      color,
		Message:    message,
		AccountSID: cfg.AccountSID,
		AuthToken:  cfg.AuthToken,
		From:       cfg.From,
		To:         cfg.To,
	}, nil
}

func (c *CodeTwilioJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(result.Err) }()
	ctx, cancel := operationContext(c.Context(), 10*time.Second)
	defer cancel()

	payload := map[string]interface{}{"type": "code", "driver": "twilio", "color": c.Color}
	form := url.Values{"To": {c.To}, "From": {c.From}, "Body": {c.Message}}
	target := "https://api.twilio.com/2010-04-01/Accounts/" + url.PathEscape(c.AccountSID) + "/Messages.json"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
	}
	req.SetBasicAuth(c.AccountSID, c.AuthToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := GetHTTPClient(10 * time.Second).Do(req)
	if err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
	}
	_ = resp.Body.Close()
	if err := notificationStatusError(resp.StatusCode); err != nil {
		return Result{ID: c.ID, Ent: c.Entity, Err: err, Payload: payload}
	}

	return Result{ID: c.ID, Ent: c.Entity, Err: nil, Payload: payload}
}

func (c *CodeTwilioJob) Copy() Job                  { job := *c; return &job }
func (c *CodeTwilioJob) GetEnqueueTime() time.Time  { return c.EnqueueTime }
func (c *CodeTwilioJob) SetEnqueueTime(t time.Time) { c.EnqueueTime = t }
func (c *CodeTwilioJob) GetStartTime() time.Time    { return c.StartTime }
func (c *CodeTwilioJob) SetStartTime(t time.Time)   { c.StartTime = t }
func (c *CodeTwilioJob) IsNil() bool                { return c == nil }
