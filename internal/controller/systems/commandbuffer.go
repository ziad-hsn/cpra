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
		Commands: make([]func(), 0, 100), // Start with reasonable capacity
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
	// Reuse the underlying array to avoid allocations
	s.Commands = s.Commands[:0]

	// Only reallocate if it grew too large
	if cap(s.Commands) > 10000 {
		s.Commands = make([]func(), 0, 100)
	}
}

func (s *CommandBufferSystem) PlayBack() {
	for _, op := range s.Commands {
		op()
	}
}

// SetPulseStatus Fixed: Properly allocate on heap without taking address of parameter
func (s *CommandBufferSystem) SetPulseStatus(entity ecs.Entity, status components.PulseStatus) {
	s.Add(func() {
		statusCopy := new(components.PulseStatus)
		*statusCopy = status
		s.PulseStatus.Set(entity, statusCopy)
	})
}

func (s *CommandBufferSystem) setMonitorStatus(entity ecs.Entity, status components.MonitorStatus) {
	s.Add(func() {
		statusCopy := new(components.MonitorStatus)
		*statusCopy = status
		s.MonitorStatus.Set(entity, statusCopy)
	})
}

func (s *CommandBufferSystem) setInterventionStatus(entity ecs.Entity, status components.InterventionStatus) {
	s.Add(func() {
		statusCopy := new(components.InterventionStatus)
		*statusCopy = status
		s.InterventionStatus.Set(entity, statusCopy)
	})
}

func (s *CommandBufferSystem) setRedCodeStatus(entity ecs.Entity, status components.RedCodeStatus) {
	s.Add(func() {
		statusCopy := new(components.RedCodeStatus)
		*statusCopy = status
		s.RedCodeStatus.Set(entity, statusCopy)
	})
}

func (s *CommandBufferSystem) setGrayCodeStatus(entity ecs.Entity, status components.GrayCodeStatus) {
	s.Add(func() {
		statusCopy := new(components.GrayCodeStatus)
		*statusCopy = status
		s.GrayCodeStatus.Set(entity, statusCopy)
	})
}

func (s *CommandBufferSystem) setGreenCodeStatus(entity ecs.Entity, status components.GreenCodeStatus) {
	s.Add(func() {
		statusCopy := new(components.GreenCodeStatus)
		*statusCopy = status
		s.GreenCodeStatus.Set(entity, statusCopy)
	})
}

func (s *CommandBufferSystem) setYellowCodeStatus(entity ecs.Entity, status components.YellowCodeStatus) {
	s.Add(func() {
		statusCopy := new(components.YellowCodeStatus)
		*statusCopy = status
		s.YellowCodeStatus.Set(entity, statusCopy)
	})
}

func (s *CommandBufferSystem) setCyanCodeStatus(entity ecs.Entity, status components.CyanCodeStatus) {
	s.Add(func() {
		statusCopy := new(components.CyanCodeStatus)
		*statusCopy = status
		s.CyanCodeStatus.Set(entity, statusCopy)
	})
}

func (s *CommandBufferSystem) schedulePulse(entity ecs.Entity) {
	s.Add(func() {
		s.PulseNeeded.Assign(entity, &components.PulseNeeded{})
	})
}

func (s *CommandBufferSystem) removeFirstCheck(entity ecs.Entity) {
	s.Add(func() {
		s.PulseFirstCheck.Remove(entity)
	})
}

func (s *CommandBufferSystem) MarkPulsePending(entity ecs.Entity) {
	s.Add(func() {
		s.World.Exchange(
			entity,
			[]ecs.ID{ecs.ComponentID[components.PulsePending](s.World)},
			[]ecs.ID{ecs.ComponentID[components.PulseNeeded](s.World)},
		)
	})
}

func (s *CommandBufferSystem) RemovePulsePending(entity ecs.Entity) {
	s.Add(func() {
		s.PulsePending.Remove(entity)
	})
}

func (s *CommandBufferSystem) scheduleIntervention(entity ecs.Entity) {
	s.Add(func() {
		s.InterventionNeeded.Assign(entity, &components.InterventionNeeded{})
	})
}

func (s *CommandBufferSystem) markInterventionPending(entity ecs.Entity) {
	s.Add(func() {
		s.World.Exchange(
			entity,
			[]ecs.ID{ecs.ComponentID[components.InterventionPending](s.World)},
			[]ecs.ID{ecs.ComponentID[components.InterventionNeeded](s.World)},
		)
	})
}

func (s *CommandBufferSystem) RemoveInterventionPending(entity ecs.Entity) {
	s.Add(func() {
		s.InterventionPending.Remove(entity)
	})
}

func (s *CommandBufferSystem) scheduleCode(entity ecs.Entity, color string) {
	// Capture color by value in the closure
	s.Add(func() {
		s.CodeNeeded.Assign(entity, &components.CodeNeeded{Color: color})
	})
}

func (s *CommandBufferSystem) MarkCodePending(entity ecs.Entity, color string) {
	// Capture both entity and color by value
	s.Add(func() {
		s.World.ExchangeFn(entity,
			[]ecs.ID{ecs.ComponentID[components.CodePending](s.World)},
			[]ecs.ID{ecs.ComponentID[components.CodeNeeded](s.World)},
			func(e ecs.Entity) {
				s.CodePending.Set(e, &components.CodePending{Color: color})
			})
	})
}

func (s *CommandBufferSystem) RemoveCodePending(entity ecs.Entity) {
	s.Add(func() {
		s.World.Remove(entity, ecs.ComponentID[components.CodePending](s.World))
	})
}
