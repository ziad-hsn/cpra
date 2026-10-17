package queue

import (
	"errors"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"time"
)

type poolTask struct{ pending *pendingEntry }

type fairSource interface {
	acquire(int) (jobs.Job, error)
	notifyChannel() <-chan struct{}
}

func (p *DynamicWorkerPool) dispatchFair() {
	for {
		q, ok := p.Queue().(fairSource)
		if !ok {
			panic("fair dispatch requires a fair queue")
		}
		job, err := q.acquire(p.antsPool.Cap())
		if errors.Is(err, ErrQueueClosed) && p.stopping.Load() != 0 {
			return
		}
		if err != nil && p.logger != nil {
			p.logger.Printf("Fair queue admission failed: %v", err)
		}
		if job == nil {
			if p.stopping.Load() != 0 && p.Queue().Stats().QueueDepth == 0 {
				return
			}
			timer := time.NewTimer(5 * time.Millisecond)
			select {
			case <-q.notifyChannel():
			case <-timer.C:
			case <-p.ctx.Done():
				timer.Stop()
				return
			}
			timer.Stop()
			continue
		}
		pending := p.pendingSubmissions.begin(job)
		p.pendingSubmissions.finish(pending, true)
		p.tasksSubmitted.Add(1)
		p.workerWG.Add(1)
		for {
			if p.antsPool.Invoke(&poolTask{pending: pending}) == nil {
				break
			}
			time.Sleep(time.Millisecond)
		}
	}
}
