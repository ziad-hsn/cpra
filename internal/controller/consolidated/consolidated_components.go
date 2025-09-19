package consolidated

import (
	"cpra/internal/jobs"
	"cpra/internal/loader/schema"
	"errors"
	"strings"
	"sync/atomic"
	"time"
)

// MonitorState consolidates all monitor state into single component
// This approach dramatically reduces archetype fragmentation and improves cache locality
type MonitorState struct {
	// Entity identification
	Name string

	// State flags (bitfield for efficiency) - replaces multiple tag components
	Flags uint32

	// Timing data
	LastCheckTime   time.Time
	LastSuccessTime time.Time
	NextCheckTime   time.Time

	// Error tracking
	ConsecutiveFailures int
	LastError           error

	// Performance padding to cache line boundary (64 bytes)
	_ [16]byte
}

// State flag constants - replaces separate components like PulseNeeded, PulsePending, etc.
const (
	StateDisabled         uint32 = 1 << 0
	StatePulseNeeded      uint32 = 1 << 1
	StatePulsePending     uint32 = 1 << 2
	StatePulseFirstCheck  uint32 = 1 << 3
	StateInterventionNeeded uint32 = 1 << 4
	StateInterventionPending uint32 = 1 << 5
	StateCodeNeeded       uint32 = 1 << 6
	StateCodePending      uint32 = 1 << 7
	// Room for more states without adding components
)

// Efficient state management methods using atomic operations
func (m *MonitorState) IsDisabled() bool { return atomic.LoadUint32(&m.Flags)&StateDisabled != 0 }
func (m *MonitorState) IsPulseNeeded() bool { return atomic.LoadUint32(&m.Flags)&StatePulseNeeded != 0 }
func (m *MonitorState) IsPulsePending() bool { return atomic.LoadUint32(&m.Flags)&StatePulsePending != 0 }
func (m *MonitorState) IsPulseFirstCheck() bool { return atomic.LoadUint32(&m.Flags)&StatePulseFirstCheck != 0 }
func (m *MonitorState) IsInterventionNeeded() bool { return atomic.LoadUint32(&m.Flags)&StateInterventionNeeded != 0 }
func (m *MonitorState) IsInterventionPending() bool { return atomic.LoadUint32(&m.Flags)&StateInterventionPending != 0 }
func (m *MonitorState) IsCodeNeeded() bool { return atomic.LoadUint32(&m.Flags)&StateCodeNeeded != 0 }
func (m *MonitorState) IsCodePending() bool { return atomic.LoadUint32(&m.Flags)&StateCodePending != 0 }

func (m *MonitorState) SetDisabled(disabled bool) {
	if disabled {
		atomic.OrUint32(&m.Flags, StateDisabled)
	} else {
		atomic.AndUint32(&m.Flags, ^StateDisabled)
	}
}

func (m *MonitorState) SetPulseNeeded(needed bool) {
	if needed {
		atomic.OrUint32(&m.Flags, StatePulseNeeded)
	} else {
		atomic.AndUint32(&m.Flags, ^StatePulseNeeded)
	}
}

func (m *MonitorState) SetPulsePending(pending bool) {
	if pending {
		atomic.OrUint32(&m.Flags, StatePulsePending)
	} else {
		atomic.AndUint32(&m.Flags, ^StatePulsePending)
	}
}

func (m *MonitorState) SetPulseFirstCheck(firstCheck bool) {
	if firstCheck {
		atomic.OrUint32(&m.Flags, StatePulseFirstCheck)
	} else {
		atomic.AndUint32(&m.Flags, ^StatePulseFirstCheck)
	}
}

func (m *MonitorState) SetInterventionNeeded(needed bool) {
	if needed {
		atomic.OrUint32(&m.Flags, StateInterventionNeeded)
	} else {
		atomic.AndUint32(&m.Flags, ^StateInterventionNeeded)
	}
}

func (m *MonitorState) SetInterventionPending(pending bool) {
	if pending {
		atomic.OrUint32(&m.Flags, StateInterventionPending)
	} else {
		atomic.AndUint32(&m.Flags, ^StateInterventionPending)
	}
}

func (m *MonitorState) SetCodeNeeded(needed bool) {
	if needed {
		atomic.OrUint32(&m.Flags, StateCodeNeeded)
	} else {
		atomic.AndUint32(&m.Flags, ^StateCodeNeeded)
	}
}

func (m *MonitorState) SetCodePending(pending bool) {
	if pending {
		atomic.OrUint32(&m.Flags, StateCodePending)
	} else {
		atomic.AndUint32(&m.Flags, ^StateCodePending)
	}
}

// PulseConfig consolidates pulse configuration
type PulseConfig struct {
	Type        string
	Timeout     time.Duration
	Interval    time.Duration
	Retries     int
	MaxFailures int
	Config      schema.PulseConfig
}

func (c *PulseConfig) Copy() *PulseConfig {
	if c == nil {
		return nil
	}
	cpy := &PulseConfig{
		Type:        strings.Clone(c.Type),
		Timeout:     c.Timeout,
		Interval:    c.Interval,
		Retries:     c.Retries,
		MaxFailures: c.MaxFailures,
	}

	if c.Config != nil {
		cpy.Config = c.Config.Copy()
	}
	return cpy
}

// InterventionConfig consolidates intervention configuration
type InterventionConfig struct {
	Action      string
	MaxFailures int
	Target      schema.InterventionTarget
}

func (c *InterventionConfig) Copy() *InterventionConfig {
	if c == nil {
		return nil
	}
	cpy := &InterventionConfig{
		Action:      strings.Clone(c.Action),
		MaxFailures: c.MaxFailures,
	}

	if c.Target != nil {
		cpy.Target = c.Target.Copy()
	}
	return cpy
}

// CodeConfig consolidates all code configurations instead of separate color components
// This single component replaces RedCodeConfig, GreenCodeConfig, CyanCodeConfig, etc.
type CodeConfig struct {
	// Color-specific configurations stored as map instead of separate components
	Configs map[string]*ColorCodeConfig
}

type ColorCodeConfig struct {
	Dispatch    bool
	MaxFailures int
	Notify      string
	Config      schema.CodeNotification
}

func (c *ColorCodeConfig) Copy() *ColorCodeConfig {
	if c == nil {
		return nil
	}
	cpy := &ColorCodeConfig{
		Dispatch:    c.Dispatch,
		MaxFailures: c.MaxFailures,
		Notify:      strings.Clone(c.Notify),
	}
	if c.Config != nil {
		cpy.Config = c.Config.Copy()
	}
	return cpy
}

func (c *CodeConfig) Copy() *CodeConfig {
	if c == nil {
		return nil
	}
	cpy := &CodeConfig{
		Configs: make(map[string]*ColorCodeConfig),
	}
	for color, config := range c.Configs {
		cpy.Configs[color] = config.Copy()
	}
	return cpy
}

// CodeStatus consolidates all code status instead of separate color status components
type CodeStatus struct {
	// Color-specific status stored as map
	Status map[string]*ColorCodeStatus
}

type ColorCodeStatus struct {
	LastStatus          string
	ConsecutiveFailures int
	LastAlertTime       time.Time
	LastSuccessTime     time.Time
	LastError           error
}

func (s *ColorCodeStatus) SetSuccess(t time.Time) {
	s.LastStatus = "success"
	s.LastError = nil
	s.ConsecutiveFailures = 0
	s.LastSuccessTime = t
	s.LastAlertTime = t
}

func (s *ColorCodeStatus) SetFailure(err error) {
	s.LastStatus = "failed"
	s.LastError = err
	s.ConsecutiveFailures++
}

func (s *ColorCodeStatus) Copy() *ColorCodeStatus {
	if s == nil {
		return nil
	}
	cpy := &ColorCodeStatus{
		LastStatus:          strings.Clone(s.LastStatus),
		ConsecutiveFailures: s.ConsecutiveFailures,
		LastAlertTime:       s.LastAlertTime,
		LastSuccessTime:     s.LastSuccessTime,
	}
	if s.LastError != nil {
		cpy.LastError = errors.New(s.LastError.Error())
	}
	return cpy
}

func (c *CodeStatus) Copy() *CodeStatus {
	if c == nil {
		return nil
	}
	cpy := &CodeStatus{
		Status: make(map[string]*ColorCodeStatus),
	}
	for color, status := range c.Status {
		cpy.Status[color] = status.Copy()
	}
	return cpy
}

// JobStorage consolidates all job storage instead of separate job components
// This single component replaces PulseJob, InterventionJob, CodeJob, etc.
type JobStorage struct {
	PulseJob        jobs.Job
	InterventionJob jobs.Job
	CodeJobs        map[string]jobs.Job // Jobs for each code color
}

func (j *JobStorage) Copy() *JobStorage {
	if j == nil {
		return nil
	}
	cpy := &JobStorage{
		CodeJobs: make(map[string]jobs.Job),
	}
	if j.PulseJob != nil {
		cpy.PulseJob = j.PulseJob.Copy()
	}
	if j.InterventionJob != nil {
		cpy.InterventionJob = j.InterventionJob.Copy()
	}
	for color, job := range j.CodeJobs {
		if job != nil {
			cpy.CodeJobs[color] = job.Copy()
		}
	}
	return cpy
}

// Legacy compatibility - keeping minimal existing components that are still needed
// These will be gradually phased out