package systems

import (
	"cpra/internal/controller/components"
	"cpra/internal/controller/entities"
	"cpra/internal/jobs"

	"github.com/mlange-42/ark/ecs"
	"log"
	"time"
)

/* -----------------------------  SCHEDULE  ----------------------------- */

type PulseScheduleSystem struct {
	PulseFilter *ecs.Filter2[components.PulseConfig, components.PulseStatus]
	Mapper      *entities.EntityManager
}

func (s *PulseScheduleSystem) Initialize(w *ecs.World) {
	s.PulseFilter = ecs.NewFilter2[components.PulseConfig, components.PulseStatus](w).
		Without(ecs.C[components.DisabledMonitor]()).
		Without(ecs.C[components.PulseNeeded]()).
		Without(ecs.C[components.PulsePending]()).
		Without(ecs.C[components.InterventionNeeded]()).
		Without(ecs.C[components.InterventionPending]()).
		Without(ecs.C[components.CodeNeeded]()).
		Without(ecs.C[components.CodePending]())
	s.Mapper = entities.InitializeMappers(w)
}

func (s *PulseScheduleSystem) collectWork(w *ecs.World) []ecs.Entity {
	var toCheck []ecs.Entity
	query := s.PulseFilter.Query()

	for query.Next() {
		ent := query.Entity()
		interval := s.Mapper.PulseConfig.Get(ent).Interval
		lastCheckTime := s.Mapper.PulseStatus.Get(ent).LastCheckTime

		// first‑time check?
		if s.Mapper.PulseFirstCheck.HasAll(ent) {
			toCheck = append(toCheck, ent)
			log.Printf("%v --> %v\n", time.Since(lastCheckTime), interval)
			continue
		}

		// interval check
		if time.Since(lastCheckTime) >= interval {
			toCheck = append(toCheck, ent)
			log.Printf("%v --> %v\n", time.Since(lastCheckTime), interval)
		}
	}
	return toCheck
}

func (s *PulseScheduleSystem) applyWork(w *ecs.World, entities []ecs.Entity) {
	for _, ent := range entities {

		if w.Alive(ent) && !s.Mapper.PulseNeeded.HasAll(ent) {
			s.Mapper.PulseNeeded.Add(ent, &components.PulseNeeded{})
		}
	}
}

func (s *PulseScheduleSystem) Update(w *ecs.World) {
	toCheck := s.collectWork(w)
	s.applyWork(w, toCheck)
}

func (s *PulseScheduleSystem) Finalize(w *ecs.World) {}

/* -----------------------------  DISPATCH  ----------------------------- */

type dispatchablePulse struct {
	Job    jobs.Job
	Status components.PulseStatus // updated LastCheckTime copy
}

type PulseDispatchSystem struct {
	JobChan     chan<- jobs.Job
	PulseNeeded *ecs.Filter1[components.PulseNeeded]
	Mapper      *entities.EntityManager
}

func (s *PulseDispatchSystem) Initialize(w *ecs.World) {
	s.PulseNeeded = ecs.NewFilter1[components.PulseNeeded](w)
	s.Mapper = entities.InitializeMappers(w)
}

func (s *PulseDispatchSystem) collectWork(w *ecs.World) map[ecs.Entity]dispatchablePulse {
	out := make(map[ecs.Entity]dispatchablePulse)
	query := s.PulseNeeded.Query()

	for query.Next() {
		ent := query.Entity()
		job := s.Mapper.PulseJob.Get(ent).Job

		stCopy := *s.Mapper.PulseStatus.Get(ent)
		stCopy.LastCheckTime = time.Now()

		out[ent] = dispatchablePulse{Job: job, Status: stCopy}
	}
	return out
}

func (s *PulseDispatchSystem) applyWork(w *ecs.World, list map[ecs.Entity]dispatchablePulse) {

	for e, item := range list {
		select {
		case s.JobChan <- item.Job:

			s.Mapper.PulseStatus.Set(e, &item.Status)

			// first‑check removal (if present)
			if s.Mapper.PulseFirstCheck.HasAll(e) {
				s.Mapper.PulseFirstCheck.Remove(e)
			}

			if s.Mapper.PulsePending.HasAll(e) {
				name := *s.Mapper.Name.Get(e)
				log.Fatalf("Monitor %v have pending component before dispatching entity: %v\n", name, e)
			}
			//s.Mapper.PulsePendingExchange.Exchange(e, &components.PulsePending{}, &components.PulseNeeded{})
			s.Mapper.PulsePending.Add(e, &components.PulsePending{})
			s.Mapper.PulseNeeded.Remove(e)
			// exchange PulseNeeded -> PulsePending

			name := *s.Mapper.Name.Get(e)
			log.Printf("sent %s job\n", name)

		default:
			log.Printf("Job channel full, skipping dispatch for entity %v", e)
		}
	}
}

func (s *PulseDispatchSystem) Update(w *ecs.World) {
	toDispatch := s.collectWork(w)
	s.applyWork(w, toDispatch)
}

func (s *PulseDispatchSystem) Finalize(w *ecs.World) {}

/* -----------------------------  RESULT  ----------------------------- */

type PulseResultSystem struct {
	ResultChan <-chan jobs.Result
	Mapper     *entities.EntityManager
}

func (s *PulseResultSystem) Initialize(w *ecs.World) {
	s.Mapper = entities.InitializeMappers(w)
}

func (s *PulseResultSystem) collectResults() map[ecs.Entity]jobs.Result {
	out := make(map[ecs.Entity]jobs.Result)
loop:
	for {
		select {
		case res := <-s.ResultChan:
			ent := res.Entity()
			out[ent] = res
		default:
			break loop
		}
	}
	return out
}

func (s *PulseResultSystem) processResultsAndQueueStructuralChanges(w *ecs.World, results map[ecs.Entity]jobs.Result) {

	for _, res := range results {
		entity := res.Entity()

		if !w.Alive(entity) || !s.Mapper.PulsePending.HasAll(entity) {
			continue
		}

		name := *s.Mapper.Name.Get(entity)

		//fmt.Printf("entity is %v for %s pulse result.\n", entity, name)

		if res.Error() != nil {
			// ---- FAILURE ----
			maxFailures := s.Mapper.PulseConfig.Get(entity).MaxFailures
			statusCopy := *s.Mapper.PulseStatus.Get(entity)
			monitorCopy := *s.Mapper.MonitorStatus.Get(entity)

			statusCopy.LastStatus = "failed"
			statusCopy.LastError = res.Error()
			statusCopy.ConsecutiveFailures++

			// yellow code on first failure
			if statusCopy.ConsecutiveFailures == 1 &&
				s.Mapper.YellowCode.HasAll(entity) {
				log.Println("Pulse failed and need Yellow Code")
				s.Mapper.CodeNeeded.Add(entity, &components.CodeNeeded{Color: "yellow"})
			}

			// interventions
			if statusCopy.ConsecutiveFailures%maxFailures == 0 &&
				s.Mapper.InterventionConfig.HasAll(entity) {
				log.Printf("Monitor %s failed %d times and needs intervention\n", name, statusCopy.ConsecutiveFailures)
				s.Mapper.InterventionNeeded.Add(entity, &components.InterventionNeeded{})
				monitorCopy.Status = "failed"
			}

			// deferred data writes
			s.Mapper.PulseStatus.Set(entity, &statusCopy)
			s.Mapper.MonitorStatus.Set(entity, &monitorCopy)

		} else {
			// ---- SUCCESS ----
			statusCopy := *s.Mapper.PulseStatus.Get(entity)
			monitorCopy := *s.Mapper.MonitorStatus.Get(entity)

			lastStatus := statusCopy.LastStatus

			statusCopy.LastStatus = "success"
			statusCopy.LastError = nil
			statusCopy.ConsecutiveFailures = 0
			statusCopy.LastSuccessTime = time.Now()
			monitorCopy.Status = "success"

			s.Mapper.PulseStatus.Set(entity, &statusCopy)
			s.Mapper.MonitorStatus.Set(entity, &monitorCopy)

			if lastStatus != "success" && lastStatus != "" {
				s.Mapper.CodeNeeded.Add(entity, &components.CodeNeeded{Color: "green"})
			}
		}

		// always remove PulsePending
		s.Mapper.PulsePending.Remove(entity)
	}
}

func (s *PulseResultSystem) Update(w *ecs.World) {
	results := s.collectResults()
	s.processResultsAndQueueStructuralChanges(w, results)
}

func (s *PulseResultSystem) Finalize(w *ecs.World) {}
