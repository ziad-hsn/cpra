//go:build systemd

package jobs

import (
	"fmt"
	"time"

	"github.com/coreos/go-systemd/v22/dbus"
	"github.com/google/uuid"
	"github.com/mlange-42/ark/ecs"

	"cpra/internal/loader/schema"
)

// InterventionSystemdJob restarts a systemd unit on the local host.
type InterventionSystemdJob struct {
	Execution
	EnqueueTime time.Time
	StartTime   time.Time
	Unit        string
	Mode        string
	Timeout     time.Duration
	Retries     int
	Entity      ecs.Entity
	ID          uuid.UUID
}

func newInterventionSystemdJob(t *schema.InterventionTargetSystemd, retries int, entity ecs.Entity) (Job, error) {
	mode := t.Mode
	if mode == "" {
		mode = "replace"
	}
	return &InterventionSystemdJob{
		ID:      uuid.New(),
		Entity:  entity,
		Unit:    t.Unit,
		Mode:    mode,
		Timeout: t.Timeout,
		Retries: retries,
	}, nil
}

func (i *InterventionSystemdJob) Execute() (result Result) {
	defer func() { result.Err = SanitizeError(result.Err) }()
	ctx, cancel := operationContext(i.Context(), i.Timeout)
	defer cancel()

	payload := map[string]interface{}{"type": "intervention", "driver": "systemd"}
	conn, err := dbus.NewSystemConnectionContext(ctx)
	if err != nil {
		return Result{ID: i.ID, Ent: i.Entity, Err: fmt.Errorf("failed to connect to systemd: %w", err), Payload: payload}
	}
	defer conn.Close()

	ch := make(chan string, 1)
	_, err = conn.RestartUnitContext(ctx, i.Unit, i.Mode, ch)
	if err != nil {
		return Result{ID: i.ID, Ent: i.Entity, Err: fmt.Errorf("failed to restart unit %q: %w", i.Unit, err), Payload: payload}
	}
	var outcome string
	select {
	case outcome = <-ch:
	case <-ctx.Done():
		return Result{ID: i.ID, Ent: i.Entity, Err: ctx.Err(), Payload: payload}
	}
	if outcome != "done" {
		return Result{ID: i.ID, Ent: i.Entity, Err: fmt.Errorf("systemd restart of %q returned %q", i.Unit, outcome), Payload: payload}
	}
	return Result{ID: i.ID, Ent: i.Entity, Err: nil, Payload: payload}
}

func (i *InterventionSystemdJob) Copy() Job                  { job := *i; return &job }
func (i *InterventionSystemdJob) GetEnqueueTime() time.Time  { return i.EnqueueTime }
func (i *InterventionSystemdJob) SetEnqueueTime(t time.Time) { i.EnqueueTime = t }
func (i *InterventionSystemdJob) GetStartTime() time.Time    { return i.StartTime }
func (i *InterventionSystemdJob) SetStartTime(t time.Time)   { i.StartTime = t }
func (i *InterventionSystemdJob) IsNil() bool                { return i == nil }
