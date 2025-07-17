package workerspool

import (
	"context"
	"cpra/internal/jobs"
	"github.com/google/uuid"
	"sync"
	"time"
)

type Pool struct {
	jobChan    chan jobs.Job
	resultChan chan jobs.Result
	heartbeat  time.Duration
}

type PoolsManager struct {
	Pools          map[uuid.UUID]struct{}
	JobChannels    map[string]chan jobs.Job
	ResultChannels map[string]chan jobs.Result
	NumWorkers     int
	Processing     map[uuid.UUID]struct{}
	Cancel         chan struct{}
	heartbeat      *time.Timer
	ctx            context.Context
	wg             *sync.WaitGroup
}

func (p PoolsManager) Init(wg *sync.WaitGroup, cancel chan struct{}, heartbeat *time.Timer) {
	p.heartbeat = heartbeat
	p.ResultChannels = make(map[string]chan jobs.Result)
	p.JobChannels = make(map[string]chan jobs.Job)
	p.Processing = make(map[uuid.UUID]struct{})
	p.ctx = context.Background()
	p.wg = wg
	p.Cancel = cancel
}

func (p PoolsManager) CreatePool() {
	uuid, err := uuid.NewRandom()
	if err != nil {
		panic(err)
	}
	p.Pools[uuid] = struct{}{}
}
