// Package components defines ECS components for the CPRA monitoring system
// Following Ark ECS best practices and memory optimization patterns
package components

import (
	"sync/atomic"
	"time"
)

// MonitorState consolidates all monitor data into a single component
// Following Ark principle: minimize component add/remove operations
// Cache-aligned for optimal memory access patterns
type MonitorState struct {
	// Core monitor configuration (immutable after creation)
	URL      string        `json:"url" yaml:"url"`
	Method   string        `json:"method" yaml:"method"`
	Interval time.Duration `json:"interval" yaml:"interval"`
	Timeout  time.Duration `json:"timeout" yaml:"timeout"`
	
	// State management (atomic operations for thread safety)
	Flags        uint32        `json:"-"` // Bitfield: Ready=1, Processing=2, Failed=4, Disabled=8
	LastCheck    time.Time     `json:"last_check"`
	NextCheck    time.Time     `json:"next_check"`
	
	// Results tracking
	StatusCode   int           `json:"status_code"`
	ResponseTime time.Duration `json:"response_time"`
	ErrorCount   uint32        `json:"error_count"`
	TotalChecks  uint64        `json:"total_checks"`
	
	// Intervention tracking
	InterventionThreshold uint32        `json:"intervention_threshold" yaml:"intervention_threshold"`
	LastIntervention      time.Time     `json:"last_intervention"`
	
	// Alert tracking
	AlertThreshold uint32    `json:"alert_threshold" yaml:"alert_threshold"`
	LastAlert      time.Time `json:"last_alert"`
	
	// Padding to cache line boundary (64 bytes total)
	_ [4]byte
}

// State flag constants (following Go naming conventions)
const (
	StateReady      uint32 = 1 << iota // Entity ready for processing
	StateProcessing                    // Entity currently being processed
	StateFailed                        // Entity in failed state
	StateDisabled                      // Entity temporarily disabled
	
	// Additional state flags for different job types
	StatePulseNeeded       // Entity needs pulse check
	StatePulsePending      // Entity has pulse job in queue
	StateInterventionNeeded // Entity needs intervention
	StateInterventionPending // Entity has intervention job in queue
	StateCodeNeeded        // Entity needs alert code
	StateCodePending       // Entity has alert job in queue
)

// Thread-safe state management methods (following Effective Go patterns)
func (m *MonitorState) IsReady() bool      { return atomic.LoadUint32(&m.Flags)&StateReady != 0 }
func (m *MonitorState) IsProcessing() bool { return atomic.LoadUint32(&m.Flags)&StateProcessing != 0 }
func (m *MonitorState) IsFailed() bool     { return atomic.LoadUint32(&m.Flags)&StateFailed != 0 }
func (m *MonitorState) IsDisabled() bool   { return atomic.LoadUint32(&m.Flags)&StateDisabled != 0 }

// Pulse state management
func (m *MonitorState) IsPulseNeeded() bool  { return atomic.LoadUint32(&m.Flags)&StatePulseNeeded != 0 }
func (m *MonitorState) IsPulsePending() bool { return atomic.LoadUint32(&m.Flags)&StatePulsePending != 0 }

// Intervention state management
func (m *MonitorState) IsInterventionNeeded() bool  { return atomic.LoadUint32(&m.Flags)&StateInterventionNeeded != 0 }
func (m *MonitorState) IsInterventionPending() bool { return atomic.LoadUint32(&m.Flags)&StateInterventionPending != 0 }

// Alert state management
func (m *MonitorState) IsCodeNeeded() bool  { return atomic.LoadUint32(&m.Flags)&StateCodeNeeded != 0 }
func (m *MonitorState) IsCodePending() bool { return atomic.LoadUint32(&m.Flags)&StateCodePending != 0 }

// State transition methods (atomic for thread safety)
func (m *MonitorState) SetReady()      { atomic.StoreUint32(&m.Flags, StateReady) }
func (m *MonitorState) SetProcessing() { atomic.StoreUint32(&m.Flags, StateProcessing) }
func (m *MonitorState) SetFailed()     { atomic.StoreUint32(&m.Flags, StateFailed) }
func (m *MonitorState) SetDisabled()   { atomic.StoreUint32(&m.Flags, StateDisabled) }

// Pulse state transitions
func (m *MonitorState) SetPulseNeeded()  { atomic.StoreUint32(&m.Flags, StatePulseNeeded) }
func (m *MonitorState) SetPulsePending() { atomic.StoreUint32(&m.Flags, StatePulsePending) }

// Intervention state transitions
func (m *MonitorState) SetInterventionNeeded()  { atomic.StoreUint32(&m.Flags, StateInterventionNeeded) }
func (m *MonitorState) SetInterventionPending() { atomic.StoreUint32(&m.Flags, StateInterventionPending) }

// Alert state transitions
func (m *MonitorState) SetCodeNeeded()  { atomic.StoreUint32(&m.Flags, StateCodeNeeded) }
func (m *MonitorState) SetCodePending() { atomic.StoreUint32(&m.Flags, StateCodePending) }

// Composite state checks for system queries
func (m *MonitorState) NeedsProcessing() bool {
	flags := atomic.LoadUint32(&m.Flags)
	return (flags&StatePulseNeeded != 0) || 
		   (flags&StateInterventionNeeded != 0) || 
		   (flags&StateCodeNeeded != 0)
}

func (m *MonitorState) HasPendingJobs() bool {
	flags := atomic.LoadUint32(&m.Flags)
	return (flags&StatePulsePending != 0) || 
		   (flags&StateInterventionPending != 0) || 
		   (flags&StateCodePending != 0)
}

// Business logic methods
func (m *MonitorState) ShouldTriggerIntervention() bool {
	return m.ErrorCount >= m.InterventionThreshold && m.InterventionThreshold > 0
}

func (m *MonitorState) ShouldTriggerAlert() bool {
	return m.ErrorCount >= m.AlertThreshold && m.AlertThreshold > 0
}

func (m *MonitorState) IsHealthy() bool {
	return m.StatusCode >= 200 && m.StatusCode < 300
}

// Performance metrics
func (m *MonitorState) GetSuccessRate() float64 {
	total := atomic.LoadUint64(&m.TotalChecks)
	if total == 0 {
		return 0.0
	}
	errors := atomic.LoadUint32(&m.ErrorCount)
	return float64(total-uint64(errors)) / float64(total)
}

func (m *MonitorState) GetAverageResponseTime() time.Duration {
	// This would need to be implemented with a moving average
	// For now, return the last response time
	return m.ResponseTime
}

// Update methods for job results
func (m *MonitorState) UpdatePulseResult(statusCode int, responseTime time.Duration, err error) {
	m.StatusCode = statusCode
	m.ResponseTime = responseTime
	m.LastCheck = time.Now()
	m.NextCheck = m.LastCheck.Add(m.Interval)
	
	if err != nil || !m.IsHealthy() {
		atomic.AddUint32(&m.ErrorCount, 1)
		m.SetFailed()
	} else {
		atomic.StoreUint32(&m.ErrorCount, 0)
		m.SetReady()
	}
	
	atomic.AddUint64(&m.TotalChecks, 1)
}

func (m *MonitorState) UpdateInterventionResult(success bool) {
	m.LastIntervention = time.Now()
	if success {
		// Reset error count after successful intervention
		atomic.StoreUint32(&m.ErrorCount, 0)
		m.SetReady()
	} else {
		m.SetFailed()
	}
}

func (m *MonitorState) UpdateAlertResult(success bool) {
	m.LastAlert = time.Now()
	// Alert doesn't change monitor state, just records the notification
}

// Validation methods
func (m *MonitorState) IsValid() bool {
	return m.URL != "" && 
		   m.Method != "" && 
		   m.Interval > 0 && 
		   m.Timeout > 0
}

// String representation for debugging
func (m *MonitorState) String() string {
	flags := atomic.LoadUint32(&m.Flags)
	return fmt.Sprintf("MonitorState{URL: %s, Flags: %d, Status: %d, Errors: %d}", 
		m.URL, flags, m.StatusCode, atomic.LoadUint32(&m.ErrorCount))
}

// Legacy component types for backward compatibility
// These are kept minimal and will be phased out

// PulseNeeded marks entities that need pulse checks
// Deprecated: Use MonitorState.StatePulseNeeded flag instead
type PulseNeeded struct{}

// PulsePending marks entities with pulse jobs in queue
// Deprecated: Use MonitorState.StatePulsePending flag instead
type PulsePending struct{}

// InterventionNeeded marks entities that need interventions
// Deprecated: Use MonitorState.StateInterventionNeeded flag instead
type InterventionNeeded struct{}

// InterventionPending marks entities with intervention jobs in queue
// Deprecated: Use MonitorState.StateInterventionPending flag instead
type InterventionPending struct{}

// CodeNeeded marks entities that need alert codes
// Deprecated: Use MonitorState.StateCodeNeeded flag instead
type CodeNeeded struct{}

// CodePending marks entities with alert jobs in queue
// Deprecated: Use MonitorState.StateCodePending flag instead
type CodePending struct{}

// Monitor represents basic monitor configuration
// Deprecated: Use MonitorState instead for better performance
type Monitor struct {
	URL      string        `json:"url" yaml:"url"`
	Method   string        `json:"method" yaml:"method"`
	Interval time.Duration `json:"interval" yaml:"interval"`
	Timeout  time.Duration `json:"timeout" yaml:"timeout"`
}

// PulseJob represents pulse job data
// Deprecated: Use queue.Job instead
type PulseJob struct {
	URL     string        `json:"url"`
	Method  string        `json:"method"`
	Timeout time.Duration `json:"timeout"`
}

// InterventionJob represents intervention job data
// Deprecated: Use queue.Job instead
type InterventionJob struct {
	URL     string        `json:"url"`
	Method  string        `json:"method"`
	Timeout time.Duration `json:"timeout"`
}

// CodeJob represents alert job data
// Deprecated: Use queue.Job instead
type CodeJob struct {
	URL     string        `json:"url"`
	Method  string        `json:"method"`
	Timeout time.Duration `json:"timeout"`
}

