package queue

// QueueType represents the type of queue to create
type QueueType string

const (
	QueueTypeAdaptive QueueType = "adaptive"
	QueueTypeWorkiva  QueueType = "workiva"
	QueueTypeHybrid   QueueType = "hybrid"
)

// QueueConfig holds configuration for queue creation
type QueueConfig struct {
	Name         string
	Type         QueueType
	Capacity     int
	HybridConfig HybridQueueConfig
}

// NewQueue creates a new queue based on the provided configuration
func NewQueue(config QueueConfig) (Queue, error) {
	switch config.Type {
	case QueueTypeAdaptive:
		capacity := config.Capacity
		if capacity <= 0 {
			capacity = defaultRingCapacity
		}
		return NewAdaptiveQueue(uint64(capacity))
	case QueueTypeWorkiva:
		capacity := config.Capacity
		if capacity <= 0 {
			capacity = defaultRingCapacity
		}
		return NewWorkivaQueue(capacity), nil
	case QueueTypeHybrid:
		hybridCfg := config.HybridConfig
		if hybridCfg.Name == "" {
			hybridCfg.Name = config.Name
		}
		if hybridCfg.RingCapacity == 0 && config.Capacity > 0 {
			hybridCfg.RingCapacity = config.Capacity
		}
		return NewHybridQueue(hybridCfg)
	default:
		hybridCfg := config.HybridConfig
		if hybridCfg.Name == "" {
			hybridCfg.Name = config.Name
		}
		return NewHybridQueue(hybridCfg)
	}
}

// DefaultQueueConfig returns the default queue configuration
func DefaultQueueConfig() QueueConfig {
	return QueueConfig{
		Name:         "hybrid",
		Type:         QueueTypeHybrid,
		Capacity:     defaultRingCapacity,
		HybridConfig: DefaultHybridQueueConfig(),
	}
}
