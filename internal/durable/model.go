// Package durable owns serializable monitor state and deterministic transitions.
// Runtime jobs, provider configuration, clients and ECS entity IDs never enter it.
package durable

import (
	"cpra/internal/slo"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"time"
)

const FormatVersion = 1

type ActionState string

const (
	Queued    ActionState = "queued"
	Started   ActionState = "started"
	Succeeded ActionState = "succeeded"
	Failed    ActionState = "failed"
	Unknown   ActionState = "unknown"
	Cancelled ActionState = "cancelled"
)

// Policy is deliberately an allowlist of non-secret decision inputs.
type Policy struct {
	Interval       time.Duration  `json:"interval"`
	Unhealthy      int            `json:"unhealthy"`
	Healthy        int            `json:"healthy"`
	Intervention   bool           `json:"intervention"`
	Enabled        bool           `json:"enabled"`
	Cooldown       time.Duration  `json:"cooldown"`
	RecoveryBypass bool           `json:"recovery_bypass"`
	Endpoints      map[string]int `json:"endpoints,omitempty"`
}

type Action struct {
	ID         string      `json:"id"`
	Revision   string      `json:"revision"`
	Kind       string      `json:"kind"`
	Color      string      `json:"color,omitempty"`
	Endpoint   int         `json:"endpoint"`
	Attempt    int         `json:"attempt"`
	State      ActionState `json:"state"`
	NotBefore  time.Time   `json:"not_before"`
	StartedAt  time.Time   `json:"started_at,omitempty"`
	FinishedAt time.Time   `json:"finished_at,omitempty"`
	Outcome    string      `json:"outcome,omitempty"`
}

type Monitor struct {
	Removed               bool                 `json:"removed"`
	TotalChecks           uint64               `json:"total_checks"`
	SuccessfulChecks      uint64               `json:"successful_checks"`
	ID                    string               `json:"monitor_id"`
	Revision              string               `json:"revision"`
	Name                  string               `json:"name"`
	Policy                Policy               `json:"policy"`
	Sequence              uint64               `json:"sequence"`
	Generation            uint64               `json:"generation"`
	Incident              bool                 `json:"incident"`
	Recovering            bool                 `json:"recovering"`
	InterventionAttempted bool                 `json:"intervention_attempted"`
	InterventionFailures  int                  `json:"intervention_failures"`
	ConsecutiveFailures   int                  `json:"consecutive_failures"`
	PulseFailures         int                  `json:"pulse_failures"`
	RecoveryStreak        int                  `json:"recovery_streak"`
	VerifyRemaining       int                  `json:"verify_remaining"`
	VerificationAfter     uint64               `json:"verification_after"`
	LastCheck             time.Time            `json:"last_check"`
	LastSuccess           time.Time            `json:"last_success"`
	NextCheck             time.Time            `json:"next_check"`
	LastLatency           time.Duration        `json:"last_latency"`
	LatencyAvailable      bool                 `json:"latency_available"`
	Warning               bool                 `json:"warning"`
	LastOutcome           string               `json:"last_outcome,omitempty"`
	Actions               map[string]Action    `json:"actions,omitempty"`
	Cooldowns             map[string]time.Time `json:"cooldowns,omitempty"`
}

func (m Monitor) Clone() Monitor {
	m.Policy.Endpoints = maps.Clone(m.Policy.Endpoints)
	m.Actions = maps.Clone(m.Actions)
	m.Cooldowns = maps.Clone(m.Cooldowns)
	return m
}

// Command includes observation time and identity at submission, never on replay.
type Command struct {
	SLO            *slo.State `json:"slo,omitempty"`
	Kind           string     `json:"kind"`
	MonitorID      string     `json:"monitor_id,omitempty"`
	Revision       string     `json:"revision,omitempty"`
	At             time.Time  `json:"at"`
	Config         *Monitor   `json:"config,omitempty"`
	Generation     uint64     `json:"generation,omitempty"`
	ActionID       string     `json:"action_id,omitempty"`
	Outcome        string     `json:"outcome,omitempty"`
	Retryable      bool       `json:"retryable,omitempty"`
	Ambiguous      bool       `json:"ambiguous,omitempty"`
	Maintenance    bool       `json:"maintenance,omitempty"`
	Warning        bool       `json:"warning,omitempty"`
	Scheduled      time.Time  `json:"scheduled,omitempty"`
	ExecutionStart time.Time  `json:"execution_start,omitempty"`
	ExecutionEnd   time.Time  `json:"execution_end,omitempty"`
	Missed         uint64     `json:"missed,omitempty"`
	Driver         string     `json:"driver,omitempty"`
}

type Event struct {
	ID        string    `json:"id"`
	MonitorID string    `json:"monitor_id"`
	Revision  string    `json:"revision"`
	At        time.Time `json:"at"`
	Type      string    `json:"type"`
	ActionID  string    `json:"action_id,omitempty"`
	Kind      string    `json:"kind,omitempty"`
	Color     string    `json:"color,omitempty"`
	Endpoint  int       `json:"endpoint,omitempty"`
	Outcome   string    `json:"outcome,omitempty"`
}

type Result struct {
	Monitor *Monitor
	Events  []Event
	Allowed bool
	Err     error
}

func identity(parts string) string {
	h := sha256.Sum256([]byte(parts))
	return hex.EncodeToString(h[:])
}

func validateCommand(c Command) error {
	if c.At.IsZero() {
		return fmt.Errorf("command observation time is required")
	}
	if c.Kind == "recover" {
		return nil
	}
	if c.Kind == "slo" {
		if c.SLO == nil || c.SLO.ByDriver == nil {
			return fmt.Errorf("missing SLO aggregate")
		}
		return c.SLO.Validate()
	}
	if c.MonitorID == "" || c.Revision == "" {
		return fmt.Errorf("monitor identity and revision are required")
	}
	switch c.Kind {
	case "configure":
		if c.Config == nil || c.Config.ID != c.MonitorID || c.Config.Revision != c.Revision || c.Config.Policy.Interval <= 0 {
			return fmt.Errorf("invalid durable monitor configuration")
		}
	case "remove":
	case "pulse":
		if c.Generation == 0 {
			return fmt.Errorf("pulse generation is required")
		}
	case "start", "result":
		if c.ActionID == "" {
			return fmt.Errorf("action identity is required")
		}
	default:
		return fmt.Errorf("unknown durable command %q", c.Kind)
	}
	return nil
}
