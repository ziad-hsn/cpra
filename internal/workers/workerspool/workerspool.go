package workerspool

import (
	"context"
	"cpra/internal/jobs"
	"github.com/google/uuid"
	"sync"
)

type pool struct {
	PulseJobCh            chan jobs.Job
	PulseResultsCh        chan jobs.Result
	InterventionJobCh     chan jobs.Job
	InterventionResultsCh chan jobs.Result
	CodeJobCh             chan jobs.Job
	CodeResultsCh         chan jobs.Result
	NumWorkers            int
	Processing            map[uuid.UUID]struct{}
	ctx                   context.Context
	wg                    *sync.WaitGroup
}
