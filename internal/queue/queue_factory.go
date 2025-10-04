package queue

// QueueType represents the type of queue to create
type QueueType string

const (
	QueueTypeAdaptive QueueType = "adaptive"
	QueueTypeWorkiva  QueueType = "workiva"
)

// QueueConfig holds configuration for queue creation
type QueueConfig struct {
	Type     QueueType
	Capacity int
}

// NewQueue creates a new queue based on the provided configuration
func NewQueue(config QueueConfig) (Queue, error) {
	switch config.Type {
	case QueueTypeAdaptive:
		return NewAdaptiveQueue(uint64(config.Capacity))
	case QueueTypeWorkiva:
		return NewWorkivaQueue(config.Capacity), nil
	default:
		// Default to Adaptive for backward compatibility
		return NewAdaptiveQueue(uint64(config.Capacity))
	}
}

// DefaultQueueConfig returns the default queue configuration
func DefaultQueueConfig() QueueConfig {
	return QueueConfig{
		Type:     QueueTypeWorkiva,
		Capacity: 1 << 16, // 65536
	}
}
