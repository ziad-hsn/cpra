package controller

// ComponentChecker defines the interface for health-checkable components.
// Implement this interface to make a component monitorable by the Watchdog.
//
// This pattern enables:
//   - Standardized health monitoring across different component types
//   - Easy addition of new components to the watchdog
//   - Decoupled health check logic from component implementation
type ComponentChecker interface {
	// Name returns the component's name for logging/identification.
	Name() string

	// IsHealthy returns true if the component is in a healthy state.
	// This is a quick check suitable for frequent polling.
	IsHealthy() bool

	// Metrics returns detailed health metrics for the component.
	// These are used for detailed logging when status changes.
	Metrics() HealthMetrics
}

// HealthMetrics contains detailed health information for a component.
type HealthMetrics struct {
	// Running indicates if the component is actively running
	Running bool

	// Pending is the number of pending tasks/items
	Pending int64

	// Completed is the number of completed tasks/items
	Completed int64

	// Capacity is the maximum capacity (0 if unlimited)
	Capacity int

	// Utilization is the current utilization ratio (0.0 to 1.0)
	Utilization float64

	// Extra holds any component-specific metrics
	Extra map[string]interface{}
}

// NewPoolChecker creates a ComponentChecker for a named worker pool.
func NewPoolChecker(name string, pools *PoolManager, poolType string) ComponentChecker {
	return &simplePoolChecker{
		name:     name,
		pools:    pools,
		poolType: poolType,
	}
}

// simplePoolChecker is a simplified pool checker that accesses pools directly.
type simplePoolChecker struct {
	name     string
	pools    *PoolManager
	poolType string
}

func (c *simplePoolChecker) Name() string {
	return c.name
}

func (c *simplePoolChecker) IsHealthy() bool {
	var stats PoolManagerStats
	if c.pools != nil {
		stats = c.pools.Stats()
	}

	var running int
	var submitted, completed int64

	switch c.poolType {
	case "pulse":
		running = stats.Pulse.RunningWorkers
		submitted = stats.Pulse.TasksSubmitted
		completed = stats.Pulse.TasksCompleted
	case "intervention":
		running = stats.Intervention.RunningWorkers
		submitted = stats.Intervention.TasksSubmitted
		completed = stats.Intervention.TasksCompleted
	case "code":
		running = stats.Code.RunningWorkers
		submitted = stats.Code.TasksSubmitted
		completed = stats.Code.TasksCompleted
	default:
		return true
	}

	// Unhealthy if has pending work but no workers
	hasPending := submitted > completed
	hasWorkers := running > 0

	return !hasPending || hasWorkers
}

func (c *simplePoolChecker) Metrics() HealthMetrics {
	var stats PoolManagerStats
	if c.pools != nil {
		stats = c.pools.Stats()
	}

	var running int
	var submitted, completed int64

	switch c.poolType {
	case "pulse":
		running = stats.Pulse.RunningWorkers
		submitted = stats.Pulse.TasksSubmitted
		completed = stats.Pulse.TasksCompleted
	case "intervention":
		running = stats.Intervention.RunningWorkers
		submitted = stats.Intervention.TasksSubmitted
		completed = stats.Intervention.TasksCompleted
	case "code":
		running = stats.Code.RunningWorkers
		submitted = stats.Code.TasksSubmitted
		completed = stats.Code.TasksCompleted
	}

	return HealthMetrics{
		Running:   running > 0,
		Pending:   submitted - completed,
		Completed: completed,
		Extra: map[string]interface{}{
			"running_workers": running,
			"tasks_submitted": submitted,
		},
	}
}

// NewQueueChecker creates a ComponentChecker for a named queue.
func NewQueueChecker(name string, queues *QueueManager, queueType string) ComponentChecker {
	return &simpleQueueChecker{
		name:      name,
		queues:    queues,
		queueType: queueType,
	}
}

// simpleQueueChecker is a simplified queue checker that accesses queues directly.
type simpleQueueChecker struct {
	name      string
	queues    *QueueManager
	queueType string
}

func (c *simpleQueueChecker) Name() string {
	return c.name
}

func (c *simpleQueueChecker) IsHealthy() bool {
	var stats QueueManagerStats
	if c.queues != nil {
		stats = c.queues.Stats()
	}

	var depth, capacity int

	switch c.queueType {
	case "pulse":
		depth = stats.Pulse.QueueDepth
		capacity = stats.Pulse.Capacity
	case "intervention":
		depth = stats.Intervention.QueueDepth
		capacity = stats.Intervention.Capacity
	case "code":
		depth = stats.Code.QueueDepth
		capacity = stats.Code.Capacity
	default:
		return true
	}

	if capacity <= 0 {
		return true
	}

	// Unhealthy if queue utilization > 95%
	utilization := float64(depth) / float64(capacity)
	return utilization < 0.95
}

func (c *simpleQueueChecker) Metrics() HealthMetrics {
	var stats QueueManagerStats
	if c.queues != nil {
		stats = c.queues.Stats()
	}

	var depth, capacity int
	var dropped int64

	switch c.queueType {
	case "pulse":
		depth = stats.Pulse.QueueDepth
		capacity = stats.Pulse.Capacity
		dropped = stats.Pulse.Dropped
	case "intervention":
		depth = stats.Intervention.QueueDepth
		capacity = stats.Intervention.Capacity
		dropped = stats.Intervention.Dropped
	case "code":
		depth = stats.Code.QueueDepth
		capacity = stats.Code.Capacity
		dropped = stats.Code.Dropped
	}

	utilization := 0.0
	if capacity > 0 {
		utilization = float64(depth) / float64(capacity)
	}

	return HealthMetrics{
		Running:     true, // Queues are always "running"
		Pending:     int64(depth),
		Capacity:    capacity,
		Utilization: utilization,
		Extra: map[string]interface{}{
			"dropped": dropped,
		},
	}
}
