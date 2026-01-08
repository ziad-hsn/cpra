package schema

import (
	"fmt"
	"maps"
	"slices"
	"sync"
)

// TypeRegistry maps discriminator strings to factory functions.
// Used by UnmarshalYAML/JSON to avoid duplicated switch logic.
type TypeRegistry[T any] struct {
	mu    sync.RWMutex
	types map[string]func() T
}

func NewTypeRegistry[T any]() *TypeRegistry[T] {
	return &TypeRegistry[T]{types: make(map[string]func() T)}
}

func (r *TypeRegistry[T]) Register(name string, factory func() T) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.types[name] = factory
}

// New returns a new instance for the given name or an error if unregistered.
func (r *TypeRegistry[T]) New(name string) (T, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if f, ok := r.types[name]; ok {
		return f(), nil
	}
	var zero T
	return zero, fmt.Errorf("unknown type: %q (registered: %v)", name, r.Names())
}

// Names returns the registered keys (useful for error messages/tests).
func (r *TypeRegistry[T]) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Collect(maps.Keys(r.types))
}

var (
	PulseRegistry        = NewTypeRegistry[PulseConfig]()
	InterventionRegistry = NewTypeRegistry[InterventionTarget]()
	NotifyRegistry       = NewTypeRegistry[CodeNotification]()
)

func init() {
	// Pulse types
	PulseRegistry.Register("http", func() PulseConfig { return &PulseHTTPConfig{} })
	PulseRegistry.Register("tcp", func() PulseConfig { return &PulseTCPConfig{} })
	PulseRegistry.Register("icmp", func() PulseConfig { return &PulseICMPConfig{} })

	// Intervention types
	InterventionRegistry.Register("docker", func() InterventionTarget { return &InterventionTargetDocker{} })

	// Notification types
	NotifyRegistry.Register("log", func() CodeNotification { return &CodeNotificationLog{} })
	NotifyRegistry.Register("slack", func() CodeNotification { return &CodeNotificationSlack{} })
	NotifyRegistry.Register("pagerduty", func() CodeNotification { return &CodeNotificationPagerDuty{} })
}
