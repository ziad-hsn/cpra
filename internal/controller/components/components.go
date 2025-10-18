// Package components defines the consolidated Entity-Component-System (ECS) components
// for the CPRA monitoring application. This design follows the principles of data-oriented
// design to maximize performance and minimize memory usage, as required for handling
// over one million concurrent monitors.
//
// By consolidating state, configuration, and jobs into a few coarse-grained components,
// we dramatically reduce the number of archetypes in the ECS world. This leads to:
//   - Improved cache locality and iteration speed.
//   - Reduced memory fragmentation.
//   - Simplified system logic by avoiding complex component additions/removals for state transitions.
//
// State management is handled via a bitfield in the MonitorState component, allowing for
// efficient, atomic updates to an entity's status without changing its archetype.
package components

import (
	"errors"
	"strings"
	"time"

	"cpra/internal/jobs"
	"cpra/internal/loader/schema"
)

// Disabled is a zero-size tag component marking an entity as disabled.
// Using a tag allows filters to exclude disabled entities efficiently at the archetype level.
type Disabled struct{}

// MonitorState consolidates all monitor state into a single component.
// This approach dramatically reduces archetype fragmentation and improves cache locality.
type MonitorState struct {
	LastCheckTime        time.Time
	LastSuccessTime      time.Time
	NextCheckTime        time.Time
	LastError            error
	Name                 string
	PendingCode          string
	ConsecutiveFailures  int
	PulseFailures        int
	InterventionFailures int
	RecoveryStreak       int
	VerifyRemaining      int
	Flags                uint32
}

// StatePulseNeeded is a state flag constant; additional related flags follow in this block.
const (
	// Disabled moved to a tag component (components.Disabled)
	StatePulseNeeded         uint32 = 1 << 1
	StatePulsePending        uint32 = 1 << 2
	StatePulseFirstCheck     uint32 = 1 << 3
	StateInterventionNeeded  uint32 = 1 << 5
	StateInterventionPending uint32 = 1 << 6
	StateCodeNeeded          uint32 = 1 << 7
	StateCodePending         uint32 = 1 << 8
	StateIncidentOpen        uint32 = 1 << 9
	StateVerifying           uint32 = 1 << 10
	// Room for more states without adding components
)

// IsPulseNeeded reports whether a pulse is needed for the monitor; related helpers follow.
func (m *MonitorState) IsPulseNeeded() bool         { return m.Flags&StatePulseNeeded != 0 }
func (m *MonitorState) IsPulsePending() bool        { return m.Flags&StatePulsePending != 0 }
func (m *MonitorState) IsPulseFirstCheck() bool     { return m.Flags&StatePulseFirstCheck != 0 }
func (m *MonitorState) IsInterventionNeeded() bool  { return m.Flags&StateInterventionNeeded != 0 }
func (m *MonitorState) IsInterventionPending() bool { return m.Flags&StateInterventionPending != 0 }
func (m *MonitorState) IsCodeNeeded() bool          { return m.Flags&StateCodeNeeded != 0 }
func (m *MonitorState) IsCodePending() bool         { return m.Flags&StateCodePending != 0 }

func (m *MonitorState) SetPulseNeeded(needed bool) {
	if needed {
		m.Flags |= StatePulseNeeded
	} else {
		m.Flags &^= StatePulseNeeded
	}
}

func (m *MonitorState) SetPulsePending(pending bool) {
	if pending {
		m.Flags |= StatePulsePending
	} else {
		m.Flags &^= StatePulsePending
	}
}

func (m *MonitorState) SetPulseFirstCheck(firstCheck bool) {
	if firstCheck {
		m.Flags |= StatePulseFirstCheck
	} else {
		m.Flags &^= StatePulseFirstCheck
	}
}

func (m *MonitorState) SetInterventionNeeded(needed bool) {
	if needed {
		m.Flags |= StateInterventionNeeded
	} else {
		m.Flags &^= StateInterventionNeeded
	}
}

func (m *MonitorState) SetInterventionPending(pending bool) {
	if pending {
		m.Flags |= StateInterventionPending
	} else {
		m.Flags &^= StateInterventionPending
	}
}

func (m *MonitorState) SetCodeNeeded(needed bool) {
	if needed {
		m.Flags |= StateCodeNeeded
	} else {
		m.Flags &^= StateCodeNeeded
	}
}

func (m *MonitorState) SetCodePending(pending bool) {
	if pending {
		m.Flags |= StateCodePending
	} else {
		m.Flags &^= StateCodePending
	}
}

// PulseConfig consolidates pulse configuration
type PulseConfig struct {
	Config             schema.PulseConfig
	Type               string
	Timeout            time.Duration
	Interval           time.Duration
	Retries            int
	UnhealthyThreshold int
	HealthyThreshold   int
}

func (c *PulseConfig) Copy() *PulseConfig {
	if c == nil {
		return nil
	}
	cpy := &PulseConfig{
		Type:               strings.Clone(c.Type),
		Timeout:            c.Timeout,
		Interval:           c.Interval,
		Retries:            c.Retries,
		UnhealthyThreshold: c.UnhealthyThreshold,
		HealthyThreshold:   c.HealthyThreshold,
	}

	if c.Config != nil {
		cpy.Config = c.Config.Copy()
	}
	return cpy
}

// InterventionConfig consolidates intervention configuration
type InterventionConfig struct {
	Target      schema.InterventionTarget
	Action      string
	MaxFailures int
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

// CodeConfig consolidates all code configurations instead of separate color components.
// This single component replaces RedCodeConfig, GreenCodeConfig, CyanCodeConfig, etc.
type CodeConfig struct {
	// Color-specific configurations stored as map instead of separate components
	Configs map[string]*ColorCodeConfig
}

type ColorCodeConfig struct {
	Config      schema.CodeNotification
	Notify      string
	MaxFailures int
	Dispatch    bool
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
	LastAlertTime       time.Time
	LastSuccessTime     time.Time
	LastError           error
	LastStatus          string
	ConsecutiveFailures int
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

// JobStorage consolidates all job storage instead of separate job components.
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

// Result components are used to convey job completion information back to the ECS.
// They are added to entities by the result handling logic and removed by the corresponding result system.

type PulseResult struct {
	Result jobs.Result
}

type InterventionResult struct {
	Result jobs.Result
}

type CodeResult struct {
	Result jobs.Result
}
