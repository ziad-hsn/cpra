package systems

import (
	"cpra/internal/controller/components"
	"github.com/mlange-42/arche/ecs"
	"github.com/mlange-42/arche/generic"
	"sync"
)

type CommandBufferSystem struct {
	Commands            []func()
	Mux                 *sync.RWMutex
	World               *ecs.World
	MonitorStatus       generic.Map[components.MonitorStatus]
	PulseStatus         generic.Map[components.PulseStatus]
	InterventionStatus  generic.Map[components.InterventionStatus]
	RedCodeStatus       generic.Map[components.RedCodeStatus]
	GreenCodeStatus     generic.Map[components.GreenCodeStatus]
	YellowCodeStatus    generic.Map[components.YellowCodeStatus]
	CyanCodeStatus      generic.Map[components.CyanCodeStatus]
	GrayCodeStatus      generic.Map[components.GrayCodeStatus]
	PulseNeeded         generic.Map1[components.PulseNeeded]
	PulseFirstCheck     generic.Map1[components.PulseFirstCheck]
	PulsePending        generic.Map1[components.PulsePending]
	InterventionNeeded  generic.Map1[components.InterventionNeeded]
	InterventionPending generic.Map1[components.InterventionPending]
	CodeNeeded          generic.Map1[components.CodeNeeded]
	CodePending         generic.Map[components.CodePending]
}

func NewCommandBufferSystem(w *ecs.World) *CommandBufferSystem {
	return &CommandBufferSystem{
		Commands: make([]func(), 0),
		Mux:      &sync.RWMutex{},
		World:    w,
	}
}

func (s *CommandBufferSystem) Init() {
	s.MonitorStatus = generic.NewMap[components.MonitorStatus](s.World)
	s.PulseStatus = generic.NewMap[components.PulseStatus](s.World)
	s.InterventionStatus = generic.NewMap[components.InterventionStatus](s.World)
	s.RedCodeStatus = generic.NewMap[components.RedCodeStatus](s.World)
	s.GreenCodeStatus = generic.NewMap[components.GreenCodeStatus](s.World)
	s.YellowCodeStatus = generic.NewMap[components.YellowCodeStatus](s.World)
	s.CyanCodeStatus = generic.NewMap[components.CyanCodeStatus](s.World)
	s.GrayCodeStatus = generic.NewMap[components.GrayCodeStatus](s.World)
	s.PulseFirstCheck = generic.NewMap1[components.PulseFirstCheck](s.World)
	s.PulseNeeded = generic.NewMap1[components.PulseNeeded](s.World)
	s.PulsePending = generic.NewMap1[components.PulsePending](s.World)
	s.InterventionNeeded = generic.NewMap1[components.InterventionNeeded](s.World)
	s.InterventionPending = generic.NewMap1[components.InterventionPending](s.World)
	s.CodeNeeded = generic.NewMap1[components.CodeNeeded](s.World)
	s.CodePending = generic.NewMap[components.CodePending](s.World)
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
			s.PulseNeeded.Assign(e, &components.PulseNeeded{})
		}
	}(entity))

}

func (s *CommandBufferSystem) removeFirstCheck(entity ecs.Entity) {
	s.Add(func(e ecs.Entity) func() {
		return func() { s.PulseFirstCheck.Remove(e) }
	}(entity))

}

func (s *CommandBufferSystem) MarkPulsePending(entity ecs.Entity) {

	s.Add(func(e ecs.Entity) func() {
		return func() {
			s.World.Exchange(
				e,
				[]ecs.ID{ecs.ComponentID[components.PulsePending](s.World)},
				[]ecs.ID{ecs.ComponentID[components.PulseNeeded](s.World)},
			)
		}
	}(entity))

}

func (s *CommandBufferSystem) RemovePulsePending(entity ecs.Entity) {
	s.Add(func(e ecs.Entity) func() { return func() { s.PulsePending.Remove(e) } }(entity))
}

func (s *CommandBufferSystem) scheduleIntervention(entity ecs.Entity) {
	s.Add(func(e ecs.Entity) func() {
		return func() { s.InterventionNeeded.Assign(e, &components.InterventionNeeded{}) }
	}(entity))

}

func (s *CommandBufferSystem) markInterventionPending(entity ecs.Entity) {
	s.Add(func(e ecs.Entity) func() {
		return func() {
			s.World.Exchange(
				e,
				[]ecs.ID{ecs.ComponentID[components.InterventionPending](s.World)},
				[]ecs.ID{ecs.ComponentID[components.InterventionNeeded](s.World)},
			)

		}
	}(entity))
}

func (s *CommandBufferSystem) RemoveInterventionPending(entity ecs.Entity) {
	s.Add(func(e ecs.Entity) func() { return func() { s.InterventionPending.Remove(e) } }(entity))
}

func (s *CommandBufferSystem) scheduleCode(entity ecs.Entity, color string) {
	s.Add(func(e ecs.Entity, c string) func() {
		return func() { s.CodeNeeded.Assign(e, &components.CodeNeeded{Color: c}) }
	}(entity, color))
}

func (s *CommandBufferSystem) MarkCodePending(entity ecs.Entity, color string) {
	s.Add(func(e ecs.Entity, c string) func() {
		return func() {

			s.World.ExchangeFn(e, []ecs.ID{ecs.ComponentID[components.CodePending](s.World)}, []ecs.ID{ecs.ComponentID[components.CodeNeeded](s.World)}, func(entity ecs.Entity) {
				s.CodePending.Set(entity, &components.CodePending{Color: c})
			})

		}
	}(entity, color))
}

func (s *CommandBufferSystem) RemoveCodePending(entity ecs.Entity) {
	s.Add(func(e ecs.Entity) func() {
		return func() {
			s.World.Remove(entity, ecs.ComponentID[components.CodePending](s.World))
		}
	}(entity))
}
