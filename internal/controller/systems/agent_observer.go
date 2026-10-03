package systems

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

type agentConnectionReader interface {
	AgentConnection(context.Context, persistence.ActionRecord) (jobs.A2AConfig, error)
}

// LoadAgentConnections verifies file-only recovery before jobs or controller
// admission start. A missing key cannot silently abandon retained connections.
func (s *DurableSystem) LoadAgentConnections(ctx context.Context, sealer *secureconfig.Sealer) error {
	if err := management.VerifyAgentConnections(ctx, s.store, sealer); err != nil {
		return err
	}
	s.agentSealer = sealer
	s.agentStoreID = s.store.Status().NodeID
	if sealer != nil {
		s.agentObserver = newAgentObserver(&agentConnections{store: s.store, sealer: sealer, storeID: s.agentStoreID}, s.store)
	}
	return nil
}

type agentConnections struct {
	store   *persistence.Store
	sealer  *secureconfig.Sealer
	storeID string
}

func (c *agentConnections) AgentConnection(ctx context.Context, action persistence.ActionRecord) (jobs.A2AConfig, error) {
	connection, err := management.OpenAgentConnection(ctx, c.sealer, c.storeID, action)
	if err != nil && ctx.Err() == nil {
		c.store.MarkUnavailable(err)
	}
	return connection, err
}

// agentObserver reads retained remote tasks outside the ECS owner. A cursor
// bounds scanning; one request at a time bounds decrypted data and outbound I/O.
// Shutdown cancels the same context and joins before encryption/storage close.
type agentObserver struct {
	connections agentConnectionReader
	store       *persistence.Store
	ctx         context.Context
	cancel      context.CancelFunc
	done        chan struct{}
	once        sync.Once
}

func newAgentObserver(connections agentConnectionReader, store *persistence.Store) *agentObserver {
	ctx, cancel := context.WithCancel(context.Background())
	return &agentObserver{connections: connections, store: store, ctx: ctx, cancel: cancel, done: make(chan struct{})}
}

func (o *agentObserver) start() { o.once.Do(func() { go o.run() }) }

func (o *agentObserver) stop() {
	o.cancel()
	o.start()
	<-o.done
}

func (o *agentObserver) run() {
	defer close(o.done)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	after := ""
	for {
		if o.ctx.Err() != nil {
			return
		}
		after = o.scan(after)
		select {
		case <-o.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// scan advances through at most one page and four serial provider reads. Its
// cursor advances after failures too, so one failed target cannot monopolize it.
func (o *agentObserver) scan(after string) string {
	view, err := o.store.ActionSnapshotContext(o.ctx)
	if err != nil {
		return after
	}
	rows, next, err := view.Page(after, 100)
	if err != nil {
		return after
	}
	reads := 0
	for n, row := range rows {
		if o.ctx.Err() != nil {
			return after
		}
		after = row.ID
		if agentObservationPending(row) {
			_ = o.observe(row)
			reads++
		}
		if n == len(rows)-1 {
			return next
		}
		if reads == 4 {
			return after
		}
	}
	return next
}

func agentObservationPending(a persistence.ActionRecord) bool {
	if a.State != persistence.Unknown || a.ExecutorKind != "agent" || a.LocalExecutorActive ||
		a.AgentConnection == nil || a.AgentReceipt == nil || a.AgentReceipt.TaskID == "" || a.AgentReceipt.Sequence == math.MaxUint64 {
		return false
	}
	switch a.AgentReceipt.State {
	case "TASK_STATE_COMPLETED", "TASK_STATE_FAILED", "TASK_STATE_CANCELED", "TASK_STATE_REJECTED":
		return false
	default:
		return true
	}
}

func (o *agentObserver) observe(original persistence.ActionRecord) error {
	ctx, cancel := context.WithTimeout(o.ctx, 5*time.Second)
	defer cancel()
	current, found, err := o.store.ActionContext(ctx, original.ID)
	if err != nil {
		return err
	}
	if !found || !agentObservationPending(current) || current.ReviewRevision != original.ReviewRevision {
		return persistence.ErrAgentReceiptConflict
	}
	connection, err := o.connections.AgentConnection(ctx, original)
	if err != nil {
		return err
	}
	old := *original.AgentReceipt
	receipt, err := jobs.ObserveA2ATask(ctx, connection, jobs.A2AReceipt{TaskID: old.TaskID, ContextID: old.ContextID, State: old.State})
	if err != nil {
		return err // Provider failure does not change storage health or the hold.
	}
	if receipt.TaskID == old.TaskID && receipt.ContextID == old.ContextID && receipt.State == old.State {
		return nil // Unchanged polling does not grow history or Raft.
	}
	return o.store.RecordAgentObservation(ctx, original, persistence.AgentReceipt{Sequence: old.Sequence + 1,
		TaskID: receipt.TaskID, ContextID: receipt.ContextID, State: receipt.State, ObservedAt: time.Now().UTC()})
}
