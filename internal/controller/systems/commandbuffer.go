package systems

import (
	"cpra/internal/controller/components"
	"cpra/internal/controller/entities"
	"github.com/mlange-42/arche/ecs"
	"github.com/mlange-42/arche/generic"
	"log"
	"sync"
)

type CommandBufferSystem struct {
	Commands           []func()
	Mux                *sync.RWMutex
	Mapper             *entities.EntityManager
	MonitorStatus      generic.Map[components.MonitorStatus]
	PulseStatus        generic.Map[components.PulseStatus]
	InterventionStatus generic.Map[components.InterventionStatus]
	RedCodeStatus      generic.Map[components.RedCodeStatus]
	GreenCodeStatus    generic.Map[components.GreenCodeStatus]
	YellowCodeStatus   generic.Map[components.YellowCodeStatus]
	CyanCodeStatus     generic.Map[components.CyanCodeStatus]
	GrayCodeStatus     generic.Map[components.GrayCodeStatus]
}

func NewCommandBufferSystem() *CommandBufferSystem {
	return &CommandBufferSystem{
		Commands: make([]func(), 0),
		Mux:      &sync.RWMutex{},
	}
}

func (s *CommandBufferSystem) Init(m *entities.EntityManager) {
	s.Mapper = m
	s.MonitorStatus = generic.NewMap[components.MonitorStatus](m.World)
	s.PulseStatus = generic.NewMap[components.PulseStatus](m.World)
	s.InterventionStatus = generic.NewMap[components.InterventionStatus](m.World)
	s.RedCodeStatus = generic.NewMap[components.RedCodeStatus](m.World)
	s.GreenCodeStatus = generic.NewMap[components.GreenCodeStatus](m.World)
	s.YellowCodeStatus = generic.NewMap[components.YellowCodeStatus](m.World)
	s.CyanCodeStatus = generic.NewMap[components.CyanCodeStatus](m.World)
	s.GrayCodeStatus = generic.NewMap[components.GrayCodeStatus](m.World)
}
func (s *CommandBufferSystem) Add(command func()) {

	s.Commands = append(s.Commands, command)
}

func (s *CommandBufferSystem) Clear() {

	s.Commands = make([]func(), 0)
}

func (s *CommandBufferSystem) PlayBack() {

	for _, op := range s.Commands {
		op()
	}
}

func (s *CommandBufferSystem) SetPulseStatus(entity ecs.Entity, status components.PulseStatus) {
	s.Add(func(e ecs.Entity, st components.PulseStatus) func() {
		// write updated status
		return func() {
			s.PulseStatus.Set(e, &st)
		}
	}(entity, status))
}

func (s *CommandBufferSystem) setMonitorStatus(entity ecs.Entity, status components.MonitorStatus) {
	s.Add(func(e ecs.Entity, st components.MonitorStatus) func() {
		// write updated status
		return func() {
			s.MonitorStatus.Set(e, &st)
		}
	}(entity, status))

}

func (s *CommandBufferSystem) setInterventionStatus(entity ecs.Entity, status components.InterventionStatus) {
	s.Add(func(e ecs.Entity, st components.InterventionStatus) func() {
		return func() {
			s.InterventionStatus.Set(e, &st)
		}
	}(entity, status))

}

func (s *CommandBufferSystem) setRedCodeStatus(entity ecs.Entity, status components.RedCodeStatus) {
	s.Add(func(e ecs.Entity, st components.RedCodeStatus) func() {
		return func() {
			s.RedCodeStatus.Set(e, &st)
		}
	}(entity, status))
}

func (s *CommandBufferSystem) setGrayCodeStatus(entity ecs.Entity, status components.GrayCodeStatus) {

	s.Add(func(e ecs.Entity, st components.GrayCodeStatus) func() {
		return func() {
			s.GrayCodeStatus.Set(e, &st)
		}
	}(entity, status))
}
func (s *CommandBufferSystem) setGreenCodeStatus(entity ecs.Entity, status components.GreenCodeStatus) {

	s.Add(func(e ecs.Entity, st components.GreenCodeStatus) func() {
		return func() {
			s.GreenCodeStatus.Set(e, &st)
		}
	}(entity, status))
}
func (s *CommandBufferSystem) setYellowCodeStatus(entity ecs.Entity, status components.YellowCodeStatus) {

	s.Add(func(e ecs.Entity, st components.YellowCodeStatus) func() {
		return func() {
			s.YellowCodeStatus.Set(e, &st)
		}
	}(entity, status))
}
func (s *CommandBufferSystem) setCyanCodeStatus(entity ecs.Entity, status components.CyanCodeStatus) {

	s.Add(func(e ecs.Entity, st components.CyanCodeStatus) func() {
		return func() {
			s.CyanCodeStatus.Set(e, &st)
		}
	}(entity, status))
}

func (s *CommandBufferSystem) schedulePulse(entity ecs.Entity) {
	s.Add(func(e ecs.Entity) func() {
		return func() {
			s.Mapper.PulseNeeded.Assign(e, &components.PulseNeeded{})
		}
	}(entity))

}

func (s *CommandBufferSystem) removeFirstCheck(entity ecs.Entity) {
	s.Add(func(e ecs.Entity) func() {
		return func() { s.Mapper.PulseFirstCheck.Remove(e) }
	}(entity))

}

func (s *CommandBufferSystem) MarkPulsePending(entity ecs.Entity) {

	s.Add(func(e ecs.Entity) func() {
		return func() {
			s.Mapper.World.Exchange(
				e,
				[]ecs.ID{ecs.ComponentID[components.PulsePending](s.Mapper.World)},
				[]ecs.ID{ecs.ComponentID[components.PulseNeeded](s.Mapper.World)},
			)
		}
	}(entity))

}

func (s *CommandBufferSystem) RemovePulsePending(entity ecs.Entity) {
	s.Add(func(e ecs.Entity) func() { return func() { s.Mapper.PulsePending.Remove(e) } }(entity))
}

func (s *CommandBufferSystem) scheduleIntervention(entity ecs.Entity) {
	s.Add(func(e ecs.Entity) func() {
		return func() { s.Mapper.InterventionNeeded.Assign(e, &components.InterventionNeeded{}) }
	}(entity))

}

func (s *CommandBufferSystem) markInterventionPending(entity ecs.Entity) {
	s.Add(func(e ecs.Entity) func() {
		return func() {
			s.Mapper.World.Exchange(
				e,
				[]ecs.ID{ecs.ComponentID[components.InterventionPending](s.Mapper.World)},
				[]ecs.ID{ecs.ComponentID[components.InterventionNeeded](s.Mapper.World)},
			)

		}
	}(entity))
}

func (s *CommandBufferSystem) RemoveInterventionPending(entity ecs.Entity) {
	s.Add(func(e ecs.Entity) func() { return func() { s.Mapper.InterventionPending.Remove(e) } }(entity))
}

func (s *CommandBufferSystem) scheduleCode(entity ecs.Entity, color string) {
	s.Add(func(e ecs.Entity, c string) func() {
		return func() { s.Mapper.CodeNeeded.Assign(e, &components.CodeNeeded{Color: c}) }
	}(entity, color))
}

func (s *CommandBufferSystem) MarkCodePending(entity ecs.Entity, color string) {
	s.Add(func(e ecs.Entity, c string) func() {
		return func() {

			s.Mapper.World.ExchangeFn(e, []ecs.ID{ecs.ComponentID[components.CodePending](s.Mapper.World)}, []ecs.ID{ecs.ComponentID[components.CodeNeeded](s.Mapper.World)}, func(entity ecs.Entity) {
				s.Mapper.CodePending.Get(entity).Color = c
			})

		}
	}(entity, color))
}

func (s *CommandBufferSystem) RemoveCodePending(entity ecs.Entity) {
	s.Add(func(e ecs.Entity) func() {
		return func() {
			s.Mapper.CodePending.Remove(e)
		}
	}(entity))
}
