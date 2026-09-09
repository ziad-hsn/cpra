package systems

import (
	"cpra/internal/alerts"
	"cpra/internal/controller/components"
	"cpra/internal/jobs"
	"errors"
	"fmt"
	"github.com/mlange-42/ark/ecs"
	"time"
)

const codeResultTTL = 5 * time.Minute
const maxAlertAttempts = 3

type BatchCodeResultSystem struct {
	world       *ecs.World
	logger      Logger
	stateLogger *StateLogger
	alertMgr    *alerts.Manager
	codeSched   *CodeScheduler
	stateMapper *ecs.Map[components.MonitorState]
	ResultChan  <-chan []jobs.Result
}

func NewBatchCodeResultSystem(w *ecs.World, ch <-chan []jobs.Result, sched *CodeScheduler, logger Logger, sl *StateLogger, mgr *alerts.Manager) *BatchCodeResultSystem {
	return &BatchCodeResultSystem{w, logger, sl, mgr, sched, ecs.NewMap[components.MonitorState](w), ch}
}
func (s *BatchCodeResultSystem) Initialize(*ecs.World) {}
func (s *BatchCodeResultSystem) Finalize(*ecs.World)   {}
func (s *BatchCodeResultSystem) Update(*ecs.World) {
	closed := false
loop:
	for s.ResultChan != nil {
		select {
		case batch, ok := <-s.ResultChan:
			if !ok {
				s.ResultChan = nil
				closed = true
				break loop
			}
			s.ProcessBatch(batch)
		default:
			break loop
		}
	}
	now := time.Now()
	for ent := range s.codeSched.active {
		if !s.world.Alive(ent) {
			delete(s.codeSched.active, ent)
			continue
		}
		state := s.stateMapper.Get(ent)
		for color, d := range state.Deliveries {
			if closed || !now.Before(d.Deadline) {
				waiting := state.PendingAlerts[:0]
				for _, r := range state.PendingAlerts {
					if r.Color != color {
						waiting = append(waiting, r)
					}
				}
				state.PendingAlerts = waiting
				state.PendingCode = ""
				if len(waiting) > 0 {
					state.PendingCode = waiting[0].Color
				}
				state.SetCodeNeeded(len(waiting) > 0)
				s.finish(ent, state, color, d, fmt.Errorf("alert delivery incomplete; outcome unknown"), false, now)
			}
		}
	}
}
func (s *BatchCodeResultSystem) ProcessBatch(batch []jobs.Result) {
	for _, result := range batch {
		ent := result.Entity()
		if !s.world.Alive(ent) {
			continue
		}
		state := s.stateMapper.Get(ent)
		if state == nil {
			continue
		}
		color := result.Color
		if color == "" {
			color, _ = result.Payload["color"].(string)
		}
		d := state.Deliveries[color]
		if d == nil || d.Generation != result.Generation || result.Endpoint < 0 || result.Endpoint >= d.Next || d.Completed[result.Endpoint] {
			continue
		}
		d.Completed[result.Endpoint] = true
		d.Seen++
		if result.Error() != nil {
			d.Failed++
			var failure *jobs.DeliveryError
			if !errors.As(result.Error(), &failure) || !failure.Retryable {
				d.Retryable = false
			}
		}
		if d.Seen < len(d.Completed) {
			continue
		}
		var err error
		if d.Failed == len(d.Completed) {
			err = fmt.Errorf("all %d notification endpoints failed", d.Failed)
		}
		s.finish(ent, state, color, d, err, d.Retryable, time.Now())
	}
}
func (s *BatchCodeResultSystem) finish(ent ecs.Entity, state *components.MonitorState, color string, d *components.CodeDelivery, err error, retryable bool, now time.Time) {
	delete(state.Deliveries, color)
	state.SetCodePending(len(state.Deliveries) > 0)
	if len(state.Deliveries) == 0 {
		delete(s.codeSched.active, ent)
	}
	if s.alertMgr != nil {
		s.alertMgr.OnResult(ent, color, err, now)
	}
	if err != nil {
		s.logger.Error("Monitor '%s' %s alert failed: %v", state.Name, color, err)
		if retryable && d.Attempts < maxAlertAttempts {
			due := now.Add(time.Second * time.Duration(1<<d.Attempts))
			state.PendingAlerts = append(state.PendingAlerts, components.AlertRequest{Color: color, NotBefore: due, Attempts: d.Attempts})
			state.SetCodeNeeded(true)
			state.PendingCode = state.PendingAlerts[0].Color
			s.codeSched.ScheduleDeferred(ent, due)
		}
	} else {
		s.logger.Info("Monitor '%s' %s alert delivered to %d endpoint(s)", state.Name, color, len(d.Completed)-d.Failed)
	}
}
