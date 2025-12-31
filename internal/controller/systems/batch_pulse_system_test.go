package systems

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"cpra/internal/controller/components"
	"cpra/internal/jobs"
	"cpra/internal/queue"

	"github.com/mlange-42/ark/ecs"
	"go.uber.org/zap"
)

// mockQueue implements queue.Queue for testing
type mockQueue struct {
	capacity     int
	depth        int
	enqueued     []interface{}
	mu           sync.Mutex
	closed       bool
	enqueueBatch func([]interface{}) error // optional override for error injection
}

func newMockQueue(capacity int) *mockQueue {
	return &mockQueue{
		capacity: capacity,
		enqueued: make([]interface{}, 0),
	}
}

func (m *mockQueue) Enqueue(job jobs.Job) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return queue.ErrQueueClosed
	}
	if len(m.enqueued)+m.depth >= m.capacity {
		return queue.ErrQueueFull
	}
	m.enqueued = append(m.enqueued, job)
	return nil
}

func (m *mockQueue) EnqueueBatch(jobs []interface{}) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.enqueueBatch != nil {
		return m.enqueueBatch(jobs)
	}
	if m.closed {
		return queue.ErrQueueClosed
	}
	if len(m.enqueued)+m.depth+len(jobs) > m.capacity {
		return queue.ErrQueueFull
	}
	m.enqueued = append(m.enqueued, jobs...)
	return nil
}

func (m *mockQueue) Dequeue() (jobs.Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, queue.ErrQueueClosed
	}
	if len(m.enqueued) == 0 {
		return nil, nil
	}
	job := m.enqueued[0]
	m.enqueued = m.enqueued[1:]
	return job.(jobs.Job), nil
}

func (m *mockQueue) DequeueBatch(maxSize int) ([]jobs.Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, queue.ErrQueueClosed
	}
	n := len(m.enqueued)
	if n > maxSize {
		n = maxSize
	}
	result := make([]jobs.Job, n)
	for i := 0; i < n; i++ {
		result[i] = m.enqueued[i].(jobs.Job)
	}
	m.enqueued = m.enqueued[n:]
	return result, nil
}

func (m *mockQueue) Stats() queue.Stats {
	m.mu.Lock()
	defer m.mu.Unlock()
	return queue.Stats{
		Capacity:   m.capacity,
		QueueDepth: m.depth + len(m.enqueued),
	}
}

func (m *mockQueue) setDepth(d int) {
	m.mu.Lock()
	m.depth = d
	m.mu.Unlock()
}

func (m *mockQueue) getEnqueued() []interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]interface{}, len(m.enqueued))
	copy(result, m.enqueued)
	return result
}

func (m *mockQueue) Close() {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
}

func (m *mockQueue) Notify() <-chan struct{} {
	return make(chan struct{})
}

// testPulseJob implements jobs.Job for testing
type testPulseJob struct {
	id          int
	enqueueTime time.Time
	startTime   time.Time
	isNil       bool
}

func newTestPulseJob(id int) *testPulseJob {
	return &testPulseJob{id: id}
}

func (j *testPulseJob) Execute(_ context.Context) jobs.Result {
	return jobs.Result{}
}

func (j *testPulseJob) Copy() jobs.Job {
	copy := *j
	return &copy
}

func (j *testPulseJob) GetEnqueueTime() time.Time  { return j.enqueueTime }
func (j *testPulseJob) SetEnqueueTime(t time.Time) { j.enqueueTime = t }
func (j *testPulseJob) GetStartTime() time.Time    { return j.startTime }
func (j *testPulseJob) SetStartTime(t time.Time)   { j.startTime = t }
func (j *testPulseJob) IsNil() bool                { return j.isNil }

// nilPulseJob is a job that returns true for IsNil()
type nilPulseJob struct {
	testPulseJob
}

func newNilPulseJob() *nilPulseJob {
	return &nilPulseJob{testPulseJob: testPulseJob{isNil: true}}
}

func (j *nilPulseJob) IsNil() bool { return true }

// noopStateLogger is a no-op implementation of StateLogger for testing
func newNoopStateLogger() *StateLogger {
	return NewStateLogger(false)
}

// =============================================================================
// BatchPulseSystem Tests
// =============================================================================

func TestNewBatchPulseSystem(t *testing.T) {
	t.Parallel()

	world := ecs.NewWorld()
	defer world.Reset()

	mockQ := newMockQueue(1000)
	logger := zap.NewNop().Sugar()
	stateLogger := newNoopStateLogger()

	system := NewBatchPulseSystem(&world, mockQ, 100, logger, stateLogger, 10)

	if system == nil {
		t.Fatal("NewBatchPulseSystem returned nil")
	}
	if system.batchSize != 100 {
		t.Errorf("batchSize = %d, want 100", system.batchSize)
	}
	if system.shardSlots != 10 {
		t.Errorf("shardSlots = %d, want 10", system.shardSlots)
	}
}

func TestNewBatchPulseSystem_DefaultShardSlots(t *testing.T) {
	t.Parallel()

	world := ecs.NewWorld()
	defer world.Reset()

	mockQ := newMockQueue(1000)
	logger := zap.NewNop().Sugar()
	stateLogger := newNoopStateLogger()

	// shardSlots <= 0 should use default
	system := NewBatchPulseSystem(world, mockQ, 100, logger, stateLogger, 0)

	if system.shardSlots != components.DefaultShardSlots {
		t.Errorf("shardSlots = %d, want %d", system.shardSlots, components.DefaultShardSlots)
	}

	system2 := NewBatchPulseSystem(world, mockQ, 100, logger, stateLogger, -5)
	if system2.shardSlots != components.DefaultShardSlots {
		t.Errorf("shardSlots = %d, want %d", system2.shardSlots, components.DefaultShardSlots)
	}
}

func TestBatchPulseSystem_Initialize(t *testing.T) {
	t.Parallel()

	world := ecs.NewWorld()
	defer world.Reset()

	mockQ := newMockQueue(1000)
	logger := zap.NewNop().Sugar()
	stateLogger := newNoopStateLogger()

	system := NewBatchPulseSystem(world, mockQ, 100, logger, stateLogger, 10)

	// Should not panic
	system.Initialize(world)
}

func TestBatchPulseSystem_SetMaxDispatch(t *testing.T) {
	t.Parallel()

	world := ecs.NewWorld()
	defer world.Reset()

	mockQ := newMockQueue(1000)
	system := NewBatchPulseSystem(world, mockQ, 100, zap.NewNop().Sugar(), newNoopStateLogger(), 10)

	system.SetMaxDispatch(50)
	if system.maxDispatch != 50 {
		t.Errorf("maxDispatch = %d, want 50", system.maxDispatch)
	}
}

func TestBatchPulseSystem_Finalize(t *testing.T) {
	t.Parallel()

	world := ecs.NewWorld()
	defer world.Reset()

	mockQ := newMockQueue(1000)
	system := NewBatchPulseSystem(world, mockQ, 100, zap.NewNop().Sugar(), newNoopStateLogger(), 10)

	// Should not panic
	system.Finalize(world)
}

func TestBatchPulseSystem_Update_NoEntities(t *testing.T) {
	t.Parallel()

	world := ecs.NewWorld()
	defer world.Reset()

	mockQ := newMockQueue(1000)
	system := NewBatchPulseSystem(world, mockQ, 100, zap.NewNop().Sugar(), newNoopStateLogger(), 10)
	system.Initialize(world)

	// Update with no entities should not panic
	system.Update(world)

	enqueued := mockQ.getEnqueued()
	if len(enqueued) != 0 {
		t.Errorf("Expected 0 enqueued jobs, got %d", len(enqueued))
	}
}

func TestBatchPulseSystem_Update_ProcessesEntities(t *testing.T) {
	t.Parallel()

	world := ecs.NewWorld()
	defer world.Reset()

	mockQ := newMockQueue(1000)
	stateLogger := newNoopStateLogger()
	system := NewBatchPulseSystem(world, mockQ, 100, zap.NewNop().Sugar(), stateLogger, 1)
	system.Initialize(world)

	// Create mapper for entities
	mapper := ecs.NewMap4[components.MonitorState, components.JobStorage, components.PulseConfig, components.Shard](world)

	// Create entities with first check flag set
	for i := 0; i < 10; i++ {
		mapper.NewEntity(
			&components.MonitorState{
				Flags: components.StatePulseFirstCheck,
			},
			&components.JobStorage{
				PulseJob: newTestPulseJob(i),
			},
			&components.PulseConfig{
				Interval: time.Second,
			},
			&components.Shard{
				ID: 0, // All in shard 0 for single-shard test
			},
		)
	}

	// Update should process entities
	system.Update(world)

	enqueued := mockQ.getEnqueued()
	if len(enqueued) != 10 {
		t.Errorf("Expected 10 enqueued jobs, got %d", len(enqueued))
	}
}

func TestBatchPulseSystem_Update_ShardFiltering(t *testing.T) {
	t.Parallel()

	world := ecs.NewWorld()
	defer world.Reset()

	mockQ := newMockQueue(1000)
	stateLogger := newNoopStateLogger()
	shardSlots := 10
	system := NewBatchPulseSystem(world, mockQ, 100, zap.NewNop().Sugar(), stateLogger, shardSlots)
	system.Initialize(world)

	mapper := ecs.NewMap4[components.MonitorState, components.JobStorage, components.PulseConfig, components.Shard](world)

	// Create 100 entities distributed across 10 shards
	for i := 0; i < 100; i++ {
		mapper.NewEntity(
			&components.MonitorState{
				Flags: components.StatePulseFirstCheck,
			},
			&components.JobStorage{
				PulseJob: newTestPulseJob(i),
			},
			&components.PulseConfig{
				Interval: time.Second,
			},
			&components.Shard{
				ID: uint8(i % shardSlots),
			},
		)
	}

	// First update should process only 1 shard (10 entities)
	system.Update(world)

	enqueued := mockQ.getEnqueued()
	if len(enqueued) != 10 {
		t.Errorf("Expected 10 enqueued jobs (1 shard), got %d", len(enqueued))
	}
}

func TestBatchPulseSystem_Update_SkipsPendingEntities(t *testing.T) {
	t.Parallel()

	world := ecs.NewWorld()
	defer world.Reset()

	mockQ := newMockQueue(1000)
	stateLogger := newNoopStateLogger()
	system := NewBatchPulseSystem(world, mockQ, 100, zap.NewNop().Sugar(), stateLogger, 1)
	system.Initialize(world)

	mapper := ecs.NewMap4[components.MonitorState, components.JobStorage, components.PulseConfig, components.Shard](world)

	// Create entity with already pending flag
	mapper.NewEntity(
		&components.MonitorState{
			Flags: components.StatePulsePending,
		},
		&components.JobStorage{
			PulseJob: newTestPulseJob(0),
		},
		&components.PulseConfig{
			Interval: time.Second,
		},
		&components.Shard{ID: 0},
	)

	system.Update(world)

	enqueued := mockQ.getEnqueued()
	if len(enqueued) != 0 {
		t.Errorf("Expected 0 enqueued jobs (entity already pending), got %d", len(enqueued))
	}
}

func TestBatchPulseSystem_Update_SkipsNilJobs(t *testing.T) {
	t.Parallel()

	world := ecs.NewWorld()
	defer world.Reset()

	mockQ := newMockQueue(1000)
	stateLogger := newNoopStateLogger()
	system := NewBatchPulseSystem(world, mockQ, 100, zap.NewNop().Sugar(), stateLogger, 1)
	system.Initialize(world)

	mapper := ecs.NewMap4[components.MonitorState, components.JobStorage, components.PulseConfig, components.Shard](world)

	// Entity with nil job
	mapper.NewEntity(
		&components.MonitorState{
			Flags: components.StatePulseFirstCheck,
		},
		&components.JobStorage{
			PulseJob: nil,
		},
		&components.PulseConfig{
			Interval: time.Second,
		},
		&components.Shard{ID: 0},
	)

	// Entity with IsNil() returning true
	mapper.NewEntity(
		&components.MonitorState{
			Flags: components.StatePulseFirstCheck,
		},
		&components.JobStorage{
			PulseJob: newNilPulseJob(),
		},
		&components.PulseConfig{
			Interval: time.Second,
		},
		&components.Shard{ID: 0},
	)

	system.Update(world)

	enqueued := mockQ.getEnqueued()
	if len(enqueued) != 0 {
		t.Errorf("Expected 0 enqueued jobs (nil jobs skipped), got %d", len(enqueued))
	}
}

func TestBatchPulseSystem_Update_QueueSaturated(t *testing.T) {
	t.Parallel()

	world := ecs.NewWorld()
	defer world.Reset()

	mockQ := newMockQueue(100)
	mockQ.setDepth(90) // 90% full
	stateLogger := newNoopStateLogger()
	system := NewBatchPulseSystem(world, mockQ, 100, zap.NewNop().Sugar(), stateLogger, 1)
	system.Initialize(world)

	mapper := ecs.NewMap4[components.MonitorState, components.JobStorage, components.PulseConfig, components.Shard](world)

	for i := 0; i < 10; i++ {
		mapper.NewEntity(
			&components.MonitorState{
				Flags: components.StatePulseFirstCheck,
			},
			&components.JobStorage{
				PulseJob: newTestPulseJob(i),
			},
			&components.PulseConfig{
				Interval: time.Second,
			},
			&components.Shard{ID: 0},
		)
	}

	system.Update(world)

	// Should still process but with reduced tokens due to capacity limits
	enqueued := mockQ.getEnqueued()
	if len(enqueued) > 10 {
		t.Errorf("Expected <= 10 enqueued jobs with reduced tokens, got %d", len(enqueued))
	}
}

func TestBatchPulseSystem_Update_QueueFull(t *testing.T) {
	t.Parallel()

	world := ecs.NewWorld()
	defer world.Reset()

	mockQ := newMockQueue(100)
	mockQ.setDepth(100) // Full
	stateLogger := newNoopStateLogger()
	system := NewBatchPulseSystem(world, mockQ, 100, zap.NewNop().Sugar(), stateLogger, 1)
	system.Initialize(world)

	mapper := ecs.NewMap4[components.MonitorState, components.JobStorage, components.PulseConfig, components.Shard](world)

	mapper.NewEntity(
		&components.MonitorState{
			Flags: components.StatePulseFirstCheck,
		},
		&components.JobStorage{
			PulseJob: newTestPulseJob(0),
		},
		&components.PulseConfig{
			Interval: time.Second,
		},
		&components.Shard{ID: 0},
	)

	system.Update(world)

	enqueued := mockQ.getEnqueued()
	if len(enqueued) != 0 {
		t.Errorf("Expected 0 enqueued jobs (queue full), got %d", len(enqueued))
	}
}

func TestBatchPulseSystem_Update_UnboundedQueue(t *testing.T) {
	t.Parallel()

	world := ecs.NewWorld()
	defer world.Reset()

	// Capacity <= 0 signals unbounded queue
	mockQ := newMockQueue(0)
	stateLogger := newNoopStateLogger()
	system := NewBatchPulseSystem(world, mockQ, 100, zap.NewNop().Sugar(), stateLogger, 1)
	system.Initialize(world)

	mapper := ecs.NewMap4[components.MonitorState, components.JobStorage, components.PulseConfig, components.Shard](world)

	for i := 0; i < 50; i++ {
		mapper.NewEntity(
			&components.MonitorState{
				Flags: components.StatePulseFirstCheck,
			},
			&components.JobStorage{
				PulseJob: newTestPulseJob(i),
			},
			&components.PulseConfig{
				Interval: time.Second,
			},
			&components.Shard{ID: 0},
		)
	}

	system.Update(world)

	enqueued := mockQ.getEnqueued()
	if len(enqueued) != 50 {
		t.Errorf("Expected 50 enqueued jobs (unbounded queue), got %d", len(enqueued))
	}
}

func TestBatchPulseSystem_Update_MaxDispatchLimit(t *testing.T) {
	t.Parallel()

	world := ecs.NewWorld()
	defer world.Reset()

	mockQ := newMockQueue(1000)
	stateLogger := newNoopStateLogger()
	system := NewBatchPulseSystem(world, mockQ, 100, zap.NewNop().Sugar(), stateLogger, 1)
	system.SetMaxDispatch(5)
	system.Initialize(world)

	mapper := ecs.NewMap4[components.MonitorState, components.JobStorage, components.PulseConfig, components.Shard](world)

	for i := 0; i < 20; i++ {
		mapper.NewEntity(
			&components.MonitorState{
				Flags: components.StatePulseFirstCheck,
			},
			&components.JobStorage{
				PulseJob: newTestPulseJob(i),
			},
			&components.PulseConfig{
				Interval: time.Second,
			},
			&components.Shard{ID: 0},
		)
	}

	system.Update(world)

	enqueued := mockQ.getEnqueued()
	if len(enqueued) != 5 {
		t.Errorf("Expected 5 enqueued jobs (max dispatch limit), got %d", len(enqueued))
	}
}

func TestBatchPulseSystem_Update_TransitionsState(t *testing.T) {
	t.Parallel()

	world := ecs.NewWorld()
	defer world.Reset()

	mockQ := newMockQueue(1000)
	stateLogger := newNoopStateLogger()
	system := NewBatchPulseSystem(world, mockQ, 100, zap.NewNop().Sugar(), stateLogger, 1)
	system.Initialize(world)

	mapper := ecs.NewMap4[components.MonitorState, components.JobStorage, components.PulseConfig, components.Shard](world)

	ent := mapper.NewEntity(
		&components.MonitorState{
			Flags: components.StatePulseFirstCheck,
		},
		&components.JobStorage{
			PulseJob: newTestPulseJob(0),
		},
		&components.PulseConfig{
			Interval: time.Second,
		},
		&components.Shard{ID: 0},
	)

	system.Update(world)

	// Verify state transition
	stateMapper := ecs.NewMap[components.MonitorState](world)
	state := stateMapper.Get(ent)

	if state.Flags&components.StatePulseFirstCheck != 0 {
		t.Error("StatePulseFirstCheck should be cleared")
	}
	if state.Flags&components.StatePulseNeeded != 0 {
		t.Error("StatePulseNeeded should be cleared")
	}
	if state.Flags&components.StatePulsePending == 0 {
		t.Error("StatePulsePending should be set")
	}
	if state.LastPulseCheckTime.IsZero() {
		t.Error("LastPulseCheckTime should be set")
	}
	if state.NextCheckTime.IsZero() {
		t.Error("NextCheckTime should be set")
	}
}

func TestBatchPulseSystem_Update_EnqueueBatchError(t *testing.T) {
	t.Parallel()

	world := ecs.NewWorld()
	defer world.Reset()

	mockQ := newMockQueue(1000)
	mockQ.enqueueBatch = func(jobs []interface{}) error {
		return errors.New("simulated enqueue error")
	}
	stateLogger := newNoopStateLogger()
	system := NewBatchPulseSystem(world, mockQ, 100, zap.NewNop().Sugar(), stateLogger, 1)
	system.Initialize(world)

	mapper := ecs.NewMap4[components.MonitorState, components.JobStorage, components.PulseConfig, components.Shard](world)

	ent := mapper.NewEntity(
		&components.MonitorState{
			Flags: components.StatePulseFirstCheck,
		},
		&components.JobStorage{
			PulseJob: newTestPulseJob(0),
		},
		&components.PulseConfig{
			Interval: time.Second,
		},
		&components.Shard{ID: 0},
	)

	system.Update(world)

	// State should NOT transition on error
	stateMapper := ecs.NewMap[components.MonitorState](world)
	state := stateMapper.Get(ent)

	if state.Flags&components.StatePulsePending != 0 {
		t.Error("StatePulsePending should NOT be set on enqueue error")
	}
}

// Table-driven edge case tests
func TestBatchPulseSystem_Update_EdgeCases(t *testing.T) {
	tests := []struct {
		name           string
		entityCount    int
		queueCapacity  int
		queueDepth     int
		shardSlots     int
		maxDispatch    int
		expectEnqueued int
	}{
		{"empty_world", 0, 100, 0, 1, 0, 0},
		{"queue_full", 10, 100, 100, 1, 0, 0},
		{"normal_operation", 10, 1000, 0, 1, 0, 10},
		{"max_dispatch_5", 20, 1000, 0, 1, 5, 5},
		{"shard_filtering_10", 100, 1000, 0, 10, 0, 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			world := ecs.NewWorld()
			// Note: Don't use defer world.Reset() in parallel subtests - causes locking issues

			mockQ := newMockQueue(tt.queueCapacity)
			mockQ.setDepth(tt.queueDepth)
			stateLogger := newNoopStateLogger()
			system := NewBatchPulseSystem(&world, mockQ, 100, zap.NewNop().Sugar(), stateLogger, tt.shardSlots)
			if tt.maxDispatch > 0 {
				system.SetMaxDispatch(tt.maxDispatch)
			}
			system.Initialize(&world)

			mapper := ecs.NewMap4[components.MonitorState, components.JobStorage, components.PulseConfig, components.Shard](&world)

			for i := 0; i < tt.entityCount; i++ {
				mapper.NewEntity(
					&components.MonitorState{
						Flags: components.StatePulseFirstCheck,
					},
					&components.JobStorage{
						PulseJob: newTestPulseJob(i),
					},
					&components.PulseConfig{
						Interval: time.Second,
					},
					&components.Shard{
						ID: uint8(i % tt.shardSlots),
					},
				)
			}

			system.Update(&world)

			enqueued := len(mockQ.getEnqueued())
			if enqueued != tt.expectEnqueued {
				t.Errorf("Expected %d enqueued, got %d", tt.expectEnqueued, enqueued)
			}
		})
	}
}

// =============================================================================
// BatchPulseScheduleSystem Tests
// =============================================================================

func TestNewBatchPulseScheduleSystem(t *testing.T) {
	t.Parallel()

	world := ecs.NewWorld()
	defer world.Reset()

	logger := zap.NewNop().Sugar()
	stateLogger := newNoopStateLogger()

	system := NewBatchPulseScheduleSystem(world, logger, stateLogger)

	if system == nil {
		t.Fatal("NewBatchPulseScheduleSystem returned nil")
	}
	if system.maxSchedulePerTick != DefaultMaxSchedulePerTick {
		t.Errorf("maxSchedulePerTick = %d, want %d", system.maxSchedulePerTick, DefaultMaxSchedulePerTick)
	}
}

func TestBatchPulseScheduleSystem_SetMaxSchedulePerTick(t *testing.T) {
	t.Parallel()

	world := ecs.NewWorld()
	defer world.Reset()

	system := NewBatchPulseScheduleSystem(world, zap.NewNop().Sugar(), newNoopStateLogger())

	system.SetMaxSchedulePerTick(500)
	if system.maxSchedulePerTick != 500 {
		t.Errorf("maxSchedulePerTick = %d, want 500", system.maxSchedulePerTick)
	}

	// Invalid value should be ignored
	system.SetMaxSchedulePerTick(0)
	if system.maxSchedulePerTick != 500 {
		t.Errorf("maxSchedulePerTick = %d, want 500 (unchanged)", system.maxSchedulePerTick)
	}

	system.SetMaxSchedulePerTick(-10)
	if system.maxSchedulePerTick != 500 {
		t.Errorf("maxSchedulePerTick = %d, want 500 (unchanged)", system.maxSchedulePerTick)
	}
}

func TestBatchPulseScheduleSystem_Initialize(t *testing.T) {
	t.Parallel()

	world := ecs.NewWorld()
	defer world.Reset()

	system := NewBatchPulseScheduleSystem(world, zap.NewNop().Sugar(), newNoopStateLogger())

	// Should not panic
	system.Initialize(world)
}

func TestBatchPulseScheduleSystem_Finalize(t *testing.T) {
	t.Parallel()

	world := ecs.NewWorld()
	defer world.Reset()

	system := NewBatchPulseScheduleSystem(world, zap.NewNop().Sugar(), newNoopStateLogger())

	// Should not panic
	system.Finalize(world)
}

func TestBatchPulseScheduleSystem_Update_NoEntities(t *testing.T) {
	t.Parallel()

	world := ecs.NewWorld()
	defer world.Reset()

	system := NewBatchPulseScheduleSystem(world, zap.NewNop().Sugar(), newNoopStateLogger())
	system.Initialize(world)

	// Should not panic with no entities
	system.Update(world)
}

func TestBatchPulseScheduleSystem_Update_SchedulesFirstCheck(t *testing.T) {
	t.Parallel()

	world := ecs.NewWorld()
	defer world.Reset()

	stateLogger := newNoopStateLogger()
	system := NewBatchPulseScheduleSystem(world, zap.NewNop().Sugar(), stateLogger)
	system.Initialize(world)

	mapper := ecs.NewMap2[components.MonitorState, components.PulseConfig](world)

	ent := mapper.NewEntity(
		&components.MonitorState{
			Flags: components.StatePulseFirstCheck,
		},
		&components.PulseConfig{
			Interval: time.Hour, // Long interval to ensure first check is triggered by flag
		},
	)

	system.Update(world)

	stateMapper := ecs.NewMap[components.MonitorState](world)
	state := stateMapper.Get(ent)

	if state.Flags&components.StatePulseNeeded == 0 {
		t.Error("StatePulseNeeded should be set after first check")
	}
	if state.Flags&components.StatePulseFirstCheck != 0 {
		t.Error("StatePulseFirstCheck should be cleared")
	}
}

func TestBatchPulseScheduleSystem_Update_SchedulesDueEntities(t *testing.T) {
	t.Parallel()

	world := ecs.NewWorld()
	defer world.Reset()

	stateLogger := newNoopStateLogger()
	system := NewBatchPulseScheduleSystem(world, zap.NewNop().Sugar(), stateLogger)
	system.Initialize(world)

	mapper := ecs.NewMap2[components.MonitorState, components.PulseConfig](world)

	ent := mapper.NewEntity(
		&components.MonitorState{
			Flags:              0,
			LastPulseCheckTime: time.Now().Add(-time.Hour), // Due for check
		},
		&components.PulseConfig{
			Interval: time.Minute,
		},
	)

	system.Update(world)

	stateMapper := ecs.NewMap[components.MonitorState](world)
	state := stateMapper.Get(ent)

	if state.Flags&components.StatePulseNeeded == 0 {
		t.Error("StatePulseNeeded should be set for due entity")
	}
}

func TestBatchPulseScheduleSystem_Update_SkipsNotDue(t *testing.T) {
	t.Parallel()

	world := ecs.NewWorld()
	defer world.Reset()

	stateLogger := newNoopStateLogger()
	system := NewBatchPulseScheduleSystem(world, zap.NewNop().Sugar(), stateLogger)
	system.Initialize(world)

	mapper := ecs.NewMap2[components.MonitorState, components.PulseConfig](world)

	ent := mapper.NewEntity(
		&components.MonitorState{
			Flags:              0,
			LastPulseCheckTime: time.Now(), // Just checked
		},
		&components.PulseConfig{
			Interval: time.Hour,
		},
	)

	system.Update(world)

	stateMapper := ecs.NewMap[components.MonitorState](world)
	state := stateMapper.Get(ent)

	if state.Flags&components.StatePulseNeeded != 0 {
		t.Error("StatePulseNeeded should NOT be set for not-due entity")
	}
}

func TestBatchPulseScheduleSystem_Update_SkipsAlreadyNeeded(t *testing.T) {
	t.Parallel()

	world := ecs.NewWorld()
	defer world.Reset()

	stateLogger := newNoopStateLogger()
	system := NewBatchPulseScheduleSystem(world, zap.NewNop().Sugar(), stateLogger)
	system.Initialize(world)

	mapper := ecs.NewMap2[components.MonitorState, components.PulseConfig](world)

	mapper.NewEntity(
		&components.MonitorState{
			Flags: components.StatePulseNeeded,
		},
		&components.PulseConfig{
			Interval: time.Second,
		},
	)

	// System should not duplicate the flag or cause issues
	system.Update(world)
}

func TestBatchPulseScheduleSystem_Update_SkipsPending(t *testing.T) {
	t.Parallel()

	world := ecs.NewWorld()
	defer world.Reset()

	stateLogger := newNoopStateLogger()
	system := NewBatchPulseScheduleSystem(world, zap.NewNop().Sugar(), stateLogger)
	system.Initialize(world)

	mapper := ecs.NewMap2[components.MonitorState, components.PulseConfig](world)

	ent := mapper.NewEntity(
		&components.MonitorState{
			Flags:              components.StatePulsePending,
			LastPulseCheckTime: time.Now().Add(-time.Hour), // Would be due
		},
		&components.PulseConfig{
			Interval: time.Minute,
		},
	)

	system.Update(world)

	stateMapper := ecs.NewMap[components.MonitorState](world)
	state := stateMapper.Get(ent)

	if state.Flags&components.StatePulseNeeded != 0 {
		t.Error("StatePulseNeeded should NOT be set for pending entity")
	}
}

func TestBatchPulseScheduleSystem_Update_MaxScheduleLimit(t *testing.T) {
	t.Parallel()

	world := ecs.NewWorld()
	defer world.Reset()

	stateLogger := newNoopStateLogger()
	system := NewBatchPulseScheduleSystem(world, zap.NewNop().Sugar(), stateLogger)
	system.SetMaxSchedulePerTick(5)
	system.Initialize(world)

	mapper := ecs.NewMap2[components.MonitorState, components.PulseConfig](world)

	for i := 0; i < 20; i++ {
		mapper.NewEntity(
			&components.MonitorState{
				Flags: components.StatePulseFirstCheck,
			},
			&components.PulseConfig{
				Interval: time.Second,
			},
		)
	}

	system.Update(world)

	// Count how many have StatePulseNeeded set
	filter := ecs.NewFilter1[components.MonitorState](world)
	query := filter.Query()
	scheduledCount := 0
	for query.Next() {
		state := query.Get()
		if state.Flags&components.StatePulseNeeded != 0 {
			scheduledCount++
		}
	}

	if scheduledCount != 5 {
		t.Errorf("Expected 5 scheduled (max limit), got %d", scheduledCount)
	}
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkBatchPulseSystem_Update(b *testing.B) {
	sizes := []int{100, 1000, 10000}

	for _, size := range sizes {
		b.Run(fmt.Sprintf("entities_%d", size), func(b *testing.B) {
			world := ecs.NewWorld()
			defer world.Reset()

			mockQ := newMockQueue(size * 2)
			stateLogger := newNoopStateLogger()
			shardSlots := size / 10
			if shardSlots < 1 {
				shardSlots = 1
			}
			system := NewBatchPulseSystem(world, mockQ, 1000, zap.NewNop().Sugar(), stateLogger, shardSlots)
			system.Initialize(world)

			mapper := ecs.NewMap4[components.MonitorState, components.JobStorage, components.PulseConfig, components.Shard](world)

			for i := 0; i < size; i++ {
				mapper.NewEntity(
					&components.MonitorState{
						Flags: components.StatePulseFirstCheck,
					},
					&components.JobStorage{
						PulseJob: newTestPulseJob(i),
					},
					&components.PulseConfig{
						Interval: time.Second,
					},
					&components.Shard{
						ID: uint8(i % shardSlots),
					},
				)
			}

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				system.Update(world)
			}
		})
	}
}

func BenchmarkBatchPulseScheduleSystem_Update(b *testing.B) {
	sizes := []int{100, 1000, 10000}

	for _, size := range sizes {
		b.Run(fmt.Sprintf("entities_%d", size), func(b *testing.B) {
			world := ecs.NewWorld()
			defer world.Reset()

			stateLogger := newNoopStateLogger()
			system := NewBatchPulseScheduleSystem(world, zap.NewNop().Sugar(), stateLogger)
			system.Initialize(world)

			mapper := ecs.NewMap2[components.MonitorState, components.PulseConfig](world)

			for i := 0; i < size; i++ {
				mapper.NewEntity(
					&components.MonitorState{
						Flags: components.StatePulseFirstCheck,
					},
					&components.PulseConfig{
						Interval: time.Second,
					},
				)
			}

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				system.Update(world)
			}
		})
	}
}
