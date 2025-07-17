package workerspool

import (
	"context"
	"cpra/internal/jobs"
	"fmt"
	"github.com/google/uuid"
	"sync"
	"time"
)

type Worker struct {
}

type Pool struct {
	alias      string
	jobChan    chan jobs.Job
	resultChan chan jobs.Result
	heartbeat  time.Duration
	workers    int
	ctx        context.Context
	cancel     context.CancelFunc
	wg         *sync.WaitGroup
}

func (p *Pool) Start() {
	for i := 0; i < p.workers; i++ {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			for {
				select {
				case <-p.ctx.Done():
					return
				case job, ok := <-p.jobChan:
					if !ok {
						return
					}
					result := job.Execute()
					p.resultChan <- result
				}
			}
		}()
	}
}

func (p *Pool) Stop() {
	p.cancel()
	close(p.jobChan)
	p.wg.Wait()
	close(p.resultChan)
}

type PoolsManager struct {
	Pools          map[string]*Pool
	JobChannels    map[string]chan jobs.Job
	ResultChannels map[string]chan jobs.Result
	NumWorkers     int
	Processing     map[uuid.UUID]struct{}
	Cancel         chan struct{}
	heartbeat      *time.Timer
	ctx            context.Context
	wg             *sync.WaitGroup
}

func (m *PoolsManager) Init(wg *sync.WaitGroup, cancel chan struct{}, heartbeat *time.Timer) {
	m.heartbeat = heartbeat
	m.ResultChannels = make(map[string]chan jobs.Result)
	m.JobChannels = make(map[string]chan jobs.Job)
	m.Processing = make(map[uuid.UUID]struct{})
	m.ctx = context.Background()
	m.wg = wg
	m.Cancel = cancel
}

func (m *PoolsManager) NewPool(alias string, workers int, jobCap, resultCap int) *Pool {
	ctx, cancel := context.WithCancel(context.Background())
	pool := &Pool{
		alias:      alias,
		jobChan:    make(chan jobs.Job, jobCap),
		resultChan: make(chan jobs.Result, resultCap),
		workers:    workers,
		ctx:        ctx,
		cancel:     cancel,
		wg:         &sync.WaitGroup{},
	}
	m.Pools[alias] = pool
	return pool

}

func (m *PoolsManager) AddPool(alias string, pool *Pool) {
	m.Pools[alias] = pool
}

func (m *PoolsManager) StartAll() {
	for _, pool := range m.Pools {
		pool.Start()
	}
}

func (m *PoolsManager) StopAll() {
	for _, pool := range m.Pools {
		pool.Stop()
	}
}

func (m *PoolsManager) GetJobChannel(alias string) (chan jobs.Job, error) {
	if pool, ok := m.Pools[alias]; ok {
		return pool.jobChan, nil
	}
	return nil, fmt.Errorf("pool %s not found.\n", alias)
}

func (m *PoolsManager) GetResultChannel(alias string) (chan jobs.Result, error) {
	if pool, ok := m.Pools[alias]; ok {
		return pool.resultChan, nil
	}
	return nil, fmt.Errorf("pool %s not found.\n", alias)
}

func NewPoolsManager() *PoolsManager {
	return &PoolsManager{Pools: map[string]*Pool{}}
}
