//go:build mongo

package jobs

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

	"cpra/internal/loader/schema"
)

// PulseMongoJob checks a MongoDB server by issuing a ping command.
type PulseMongoJob struct {
	Execution
	EnqueueTime time.Time
	StartTime   time.Time
	URI         string
	Timeout     time.Duration
	Retries     int
	Entity      ecs.Entity
	ID          uuid.UUID
	payload     map[string]interface{}
}

func newPulseMongoJob(cfg *schema.PulseMongoConfig, timeout time.Duration, entity ecs.Entity) (Job, error) {
	if strings.HasPrefix(strings.ToLower(cfg.URI), "mongodb+srv:") {
		return nil, fmt.Errorf("mongodb+srv discovery cannot honor check deadlines; use a direct mongodb:// URI")
	}
	return &PulseMongoJob{
		ID:      uuid.New(),
		Entity:  entity,
		URI:     cfg.URI,
		Timeout: timeout,
		Retries: cfg.Retries,
		payload: map[string]interface{}{"type": "pulse", "driver": "mongo"},
	}, nil
}

func (p *PulseMongoJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(result.Err) }()
	ctx, cancel := operationContext(p.Context(), p.Timeout)
	defer cancel()

	payload := p.payload
	if strings.HasPrefix(strings.ToLower(p.URI), "mongodb+srv:") {
		return Result{ID: p.ID, Ent: p.Entity, Err: fmt.Errorf("mongodb+srv discovery cannot honor check deadlines; use a direct mongodb:// URI"), Payload: payload}
	}
	// v2 API: Connect takes no context argument.
	client, err := mongo.Connect(options.Client().ApplyURI(p.URI))
	if err != nil {
		return Result{ID: p.ID, Ent: p.Entity, Err: fmt.Errorf("failed to create mongo client: %w", err), Payload: payload}
	}
	defer func() {
		_ = client.Disconnect(ctx)
	}()

	attempts := p.Retries + 1
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		err := client.Ping(ctx, readpref.Primary())
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
	return Result{ID: p.ID, Ent: p.Entity, Err: fmt.Errorf("mongo check failed after %d attempt(s): %w", attempts, lastErr), Payload: payload}
}

func (p *PulseMongoJob) Copy() Job                  { job := *p; return &job }
func (p *PulseMongoJob) GetEnqueueTime() time.Time  { return p.EnqueueTime }
func (p *PulseMongoJob) SetEnqueueTime(t time.Time) { p.EnqueueTime = t }
func (p *PulseMongoJob) GetStartTime() time.Time    { return p.StartTime }
func (p *PulseMongoJob) SetStartTime(t time.Time)   { p.StartTime = t }
func (p *PulseMongoJob) IsNil() bool                { return p == nil }
