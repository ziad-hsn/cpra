package systems

import (
	"context"
	"fmt"
	"golang.org/x/exp/constraints"
	"net/http"
	"sync"
	"time"
)

type resultTypes interface {
	int | *http.Response | bool | constraints.Integer
}

type worker[T resultTypes] interface {
	Start(jobs <-chan job[T], results chan<- response[T])
	Stop()
}

type workerPool interface {
	Scale(n int)
	Drain()
	Start()
}

type dispatcher[T resultTypes] interface {
	Start()
	Dispatch(job job[T])
}

type simpleDispatcher[T resultTypes] struct {
	workersJobs []chan job[T]
	jobs        chan job[T]
}

func (s *simpleDispatcher[T]) Start() {
	for {
		select {
		case job := <-s.jobs:
			for _, w := range s.workersJobs {
				select {
				case w <- job:
					fmt.Println("worker picked up job")
				default:
					fmt.Println("no available workers")
				}
			}
		}
	}
}
func (s *simpleDispatcher[T]) Dispatch(jobs ...job[T]) {
	for _, job := range jobs {
		s.jobs <- job
	}
}

type collector[T resultTypes] interface {
	Collect(jobs []job[T])
}

type simpleCollector[T resultTypes] struct {
	workersJobs chan job[T]
	jobs        chan job[T]
}

type stagePool interface {
	Scale(n int)
	Failover()
}

type job[T resultTypes] interface {
	Execute() response[T]
}

type testJob[T constraints.Integer] struct {
	x T
	y T
}

func (j *testJob[T]) Execute() response[T] {
	fmt.Println("Executing test job")
	return response[T]{
		out: j.x + j.y,
		err: nil,
	}
}

type response[T resultTypes] struct {
	out T
	err error
}

type testWorker[T resultTypes] struct {
	ctx    context.Context
	cancel context.CancelFunc
}

func (w *testWorker[T]) Start(jobs <-chan job[T], results chan<- response[T]) {
	_, cancel := context.WithCancel(w.ctx)
	w.cancel = cancel
	defer fmt.Println("Worker terminated")
	defer w.Stop()
	for {
		select {
		case <-w.ctx.Done():
			fmt.Println("Worker stopped via context")
			return

		case j, ok := <-jobs:
			if !ok { // Channel closed
				fmt.Println("Jobs channel closed")
				return
			}
			select {
			case results <- j.Execute():
			case <-w.ctx.Done():
				return
			}
		case <-time.After(5 * time.Second):
			fmt.Println("Worker timed out")
			return

		}
	}
}

func (w *testWorker[T]) Stop() {
	w.cancel()
}

type SimpleWorkerPool[T resultTypes] struct {
	workers  []worker[T]
	jobs     chan []job[T]
	results  chan response[T]
	wJobs    []chan job[T]
	wResults []chan response[T]
	wg       *sync.WaitGroup
	started  chan bool
}

func (s *SimpleWorkerPool[T]) Scale(n int) {
	toAdd := n - len(s.workers)
	if toAdd < 0 {
		for i := range -toAdd {
			s.workers[i].Stop()
		}
	} else {
		for range toAdd {
			w := &testWorker[T]{}
			jobs := make(chan job[T])
			go w.Start(jobs, s.results)
			s.workers = append(s.workers, w)
		}
	}
}

func (s *SimpleWorkerPool[T]) Drain() {
	for len(s.workers) > 0 {
		s.workers[0].Stop()
		s.workers = s.workers[1:]
	}
}

func (s *SimpleWorkerPool[T]) Start() {
	for _, w := range s.workers {
		job := make(chan job[T])
		result := make(chan response[T])
		s.wJobs = append(s.wJobs, job)
		s.wResults = append(s.wResults, result)
		go w.Start(job, s.results)
	}
	dispatcherJobs := make(chan job[T], len(s.wJobs))
	dispatcher := &simpleDispatcher[T]{
		workersJobs: s.wJobs,
		jobs:        dispatcherJobs,
	}

	go dispatcher.Start()

	for {
		select {
		case jobs := <-s.jobs:
			go func() {
				dispatcher.Dispatch(jobs...)
			}()
		case result := <-s.results:
			fmt.Printf("Worker received result: %v\n", result.out)
		default:
			continue
		}
	}
}

func aggregator[T resultTypes](out chan<- response[T], channels ...<-chan response[T]) {
	for _, chn := range channels {
		for res := range chn {
			out <- res
		}
	}
}
