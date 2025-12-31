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
	"strings"
	"time"

	"cpra/internal/jobs"
	"cpra/internal/loader/schema"
)

// DefaultShardSlots defines the baseline number of time-partition slots used to spread work across ticks.
// Each monitor is assigned to a shard [0, shardSlots), and only one shard is processed per tick.
const DefaultShardSlots = 100

// Shard is a lightweight component that stores the shard assignment for a monitor.
type Shard struct {
	ID uint8
}

// Disabled is a zero-size tag component marking an entity as disabled.
// Using a tag allows filters to exclude disabled entities efficiently at the archetype level.
type Disabled struct{}

// MonitorState consolidates all monitor state into a single component.
// This approach dramatically reduces archetype fragmentation and improves cache locality.
type MonitorState struct {
	LastPulseCheckTime   time.Time
	LastEventTime        time.Time
	LastSuccessTime      time.Time
	NextCheckTime        time.Time
	LastError            error
	Name                 string
	ConsecutiveFailures  int
	PulseFailures        int
	InterventionFailures int
	RecoveryStreak       int
	VerifyRemaining      int
	Flags                uint32
	PendingColor         ColorCode
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

// ColorCode represents a color for alert codes.
type ColorCode uint8

// Color constants for fixed array indexing
const (
	ColorRed    ColorCode = 0
	ColorOrange ColorCode = 1
	ColorYellow ColorCode = 2
	ColorGreen  ColorCode = 3
	ColorCyan   ColorCode = 4
	ColorBlue   ColorCode = 5
	ColorPurple ColorCode = 6
	ColorGray   ColorCode = 7
	// MaxColors must match the number of supported colors
	MaxColors ColorCode = 8
	// ColorNone indicates no pending color (sentinel value)
	ColorNone ColorCode = 255
)

// colorPriority defines alert priority (higher = more urgent)
var colorPriority = [MaxColors]uint8{
	5, // red - highest
	4, // orange
	4, // yellow
	2, // green
	2, // cyan
	1, // blue
	1, // purple
	0, // gray - lowest
}

// Priority returns the alert priority of this color (0-5, higher = more urgent)
func (c ColorCode) Priority() uint8 {
	if c >= MaxColors {
		return 0
	}
	return colorPriority[c]
}

// HigherPriorityThan returns true if this color has higher priority than other
func (c ColorCode) HigherPriorityThan(other ColorCode) bool {
	return c.Priority() > other.Priority()
}

// String returns the color name
func (c ColorCode) String() string {
	if c >= MaxColors {
		if c == ColorNone {
			return "none"
		}
		return "unknown"
	}
	return IndexToColor[c]
}

// ColorToIndex converts a color name to its ColorCode.
// Returns ColorNone if the color is not recognized.
func ColorToIndex(color string) ColorCode {
	switch color {
	case "red":
		return ColorRed
	case "orange":
		return ColorOrange
	case "yellow":
		return ColorYellow
	case "green":
		return ColorGreen
	case "cyan":
		return ColorCyan
	case "blue":
		return ColorBlue
	case "purple":
		return ColorPurple
	case "gray":
		return ColorGray
	default:
		return ColorNone
	}
}

// IndexToColor converts an index to its color name.
var IndexToColor = [MaxColors]string{
	"red", "orange", "yellow", "green", "cyan", "blue", "purple", "gray",
}

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

// CodeConfig consolidates all code configurations using a fixed array.
// This single component replaces separate map-based configurations, enabling value semantics.
type CodeConfig struct {
	// Fixed array configuration - Value Type, Zero Allocation
	Configs [MaxColors]ConfigID
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
	// Value copy of the ID array
	cpy := &CodeConfig{Configs: c.Configs}
	return cpy
}

// Get returns a pointer to the config for the given color, or nil if invalid.
func (c *CodeConfig) Get(color ColorCode) *ColorCodeConfig {
	// Deprecated: CodeConfig now stores ConfigIDs. Use Resolve with a registry instead.
	return nil
}

// CodeStatus consolidates all code status using a fixed array.
type CodeStatus struct {
	// Fixed array status - Value Type
	Status [MaxColors]ColorCodeStatus
}

// Status flags for ColorCodeStatus
const (
	StatusSuccess  uint8 = 1 << 0 // Last operation succeeded
	StatusHasError uint8 = 1 << 1 // Error occurred (check error log for details)
)

// ColorCodeStatus uses compact representation:
// - int64 Unix timestamps instead of time.Time (24 bytes -> 8 bytes each)
// - uint8 bitfield instead of string (16 bytes -> 1 byte)
// - uint16 instead of int (8 bytes -> 2 bytes)
// Total: ~80 bytes -> ~19 bytes per color
type ColorCodeStatus struct {
	LastAlertTime       int64  // Unix timestamp
	LastSuccessTime     int64  // Unix timestamp
	ConsecutiveFailures uint16 // Max 65535 failures
	Flags               uint8  // Bitfield: StatusSuccess, StatusHasError
}

func (s *ColorCodeStatus) SetSuccess(t time.Time) {
	s.Flags = StatusSuccess
	s.ConsecutiveFailures = 0
	s.LastSuccessTime = t.Unix()
	s.LastAlertTime = t.Unix()
}

func (s *ColorCodeStatus) SetFailure(_ error) {
	s.Flags = StatusHasError
	if s.ConsecutiveFailures < 65535 {
		s.ConsecutiveFailures++
	}
}

// IsSuccess returns true if last status was success
func (s *ColorCodeStatus) IsSuccess() bool {
	return s.Flags&StatusSuccess != 0
}

// GetLastAlertTime returns LastAlertTime as time.Time
func (s *ColorCodeStatus) GetLastAlertTime() time.Time {
	if s.LastAlertTime == 0 {
		return time.Time{}
	}
	return time.Unix(s.LastAlertTime, 0)
}

// GetLastSuccessTime returns LastSuccessTime as time.Time
func (s *ColorCodeStatus) GetLastSuccessTime() time.Time {
	if s.LastSuccessTime == 0 {
		return time.Time{}
	}
	return time.Unix(s.LastSuccessTime, 0)
}

func (s *ColorCodeStatus) Copy() *ColorCodeStatus {
	if s == nil {
		return nil
	}
	cpy := &ColorCodeStatus{
		ConsecutiveFailures: s.ConsecutiveFailures,
		Flags:               s.Flags,
		LastAlertTime:       s.LastAlertTime,
		LastSuccessTime:     s.LastSuccessTime,
	}
	return cpy
}

func (c *CodeStatus) Copy() *CodeStatus {
	if c == nil {
		return nil
	}
	cpy := &CodeStatus{}
	for i := ColorCode(0); i < MaxColors; i++ {
		cpy.Status[i] = *c.Status[i].Copy()
	}
	return cpy
}

// Get returns a pointer to the status for the given color, or nil if invalid.
func (c *CodeStatus) Get(color string) *ColorCodeStatus {
	idx := ColorToIndex(color)
	if idx >= MaxColors {
		return nil
	}
	return &c.Status[idx]
}

// JobStorage consolidates all job storage instead of separate job components.
// This single component replaces PulseJob, InterventionJob, CodeJob, etc.
type JobStorage struct {
	PulseJob        jobs.Job
	InterventionJob jobs.Job
}

func (j *JobStorage) Copy() *JobStorage {
	if j == nil {
		return nil
	}
	cpy := &JobStorage{}
	if j.PulseJob != nil {
		cpy.PulseJob = j.PulseJob.Copy()
	}
	if j.InterventionJob != nil {
		cpy.InterventionJob = j.InterventionJob.Copy()
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
