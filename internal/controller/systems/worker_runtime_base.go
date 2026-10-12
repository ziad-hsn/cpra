//go:build !externaljobs

package systems

import (
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
)

type systemExtensions struct{}
type checkScheduleExtensions struct{}

func (s *DurableSystem) dispatchReady(now time.Time) {
	s.dispatchChecks(now)
	s.dispatchActions(now)
}

func (s *DurableSystem) expectCheck(ent ecs.Entity, at time.Time) bool {
	s.store.SLO().Expect(s.configs.Get(ent).Type, at)
	return true
}

func (*DurableSystem) checkpointWorkerSchedules(commands []persistence.Command, _ time.Time) []persistence.Command {
	return commands
}

func (*DurableSystem) initWorkerDispatch(*management.Catalog)                   {}
func (*DurableSystem) startWorkerDispatch()                                     {}
func (*DurableSystem) stopWorkerDispatch()                                      {}
func (*DurableSystem) finishWorkerDispatch()                                    {}
func (*DurableSystem) receiveWorkerDispatch()                                   {}
func (*DurableSystem) workerNeedsUpdate() bool                                  { return false }
func (*DurableSystem) workerCheck(ecs.Entity) bool                              { return false }
func (*DurableSystem) dispatchWorkerAction(ecs.Entity, persistence.Action) bool { return false }

func (*DurableSystem) dispatchWorkerChecks()        {}
func (*DurableSystem) cancelWorkerCheck(ecs.Entity) {}

func (s *DurableSystem) enqueueCheck(ent ecs.Entity) {
	s.scheduler.EnqueueReady([]ecs.Entity{ent})
}
