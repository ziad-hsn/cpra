package jobs

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"

	"cpra/internal/loader/schema"
)

// InterventionWebhookJob triggers an external system via an HTTP request.
// Pure stdlib, always compiled.
type InterventionWebhookJob struct {
	Execution
	EnqueueTime time.Time
	StartTime   time.Time
	URL         string
	Method      string
	Headers     map[string]string
	Body        string
	Timeout     time.Duration
	Retries     int
	Entity      ecs.Entity
	ID          uuid.UUID
}

func newInterventionWebhookJob(t *schema.InterventionTargetWebhook, retries int, entity ecs.Entity) (Job, error) {
	method := t.Method
	if method == "" {
		method = http.MethodPost
	}
	return &InterventionWebhookJob{
		ID:      uuid.New(),
		Entity:  entity,
		URL:     t.URL,
		Method:  method,
		Headers: t.Headers,
		Body:    t.Body,
		Timeout: t.Timeout,
		Retries: retries,
	}, nil
}

// A webhook action has one attempt. A lost response does not prove the target
// rejected the action; operators must reconcile it before retrying.
func (i *InterventionWebhookJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(result.Err) }()
	result = Result{ID: i.ID, Ent: i.Entity, Payload: map[string]interface{}{"type": "intervention", "driver": "webhook"}}
	ctx, cancel := operationContext(i.Context(), i.Timeout)
	defer cancel()
	if err := validateTargetURL(i.URL); err != nil {
		result.Err = err
		return
	}
	method := i.Method
	if method == "" {
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, i.URL, strings.NewReader(i.Body))
	if err != nil {
		result.Err = err
		return
	}
	for k, v := range i.Headers {
		req.Header.Set(k, v)
	}
	resp, err := effectHTTPClient(i.Timeout).Do(req)
	if err != nil {
		result.Err = fmt.Errorf("webhook outcome unknown; reconcile target before retry: %w", err)
		return
	}
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result.Err = fmt.Errorf("webhook action returned HTTP %d; reconcile target before retry", resp.StatusCode)
	}
	return
}

func (i *InterventionWebhookJob) Copy() Job {
	headers := make(map[string]string, len(i.Headers))
	for k, v := range i.Headers {
		headers[k] = v
	}
	job := *i
	job.Headers = headers
	return &job
}
func (i *InterventionWebhookJob) GetEnqueueTime() time.Time  { return i.EnqueueTime }
func (i *InterventionWebhookJob) SetEnqueueTime(t time.Time) { i.EnqueueTime = t }
func (i *InterventionWebhookJob) GetStartTime() time.Time    { return i.StartTime }
func (i *InterventionWebhookJob) SetStartTime(t time.Time)   { i.StartTime = t }
func (i *InterventionWebhookJob) IsNil() bool                { return i == nil }
