package persistence

import (
	"errors"
	"maps"
	"math"
	"slices"
	"time"

	"github.com/hashicorp/raft"
)

// MaintenanceWindow pauses external actions over [Start, End). Health checks
// continue to observe the target, as with existing periodic maintenance.
type MaintenanceWindow struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

func (p Policy) Clone() Policy {
	p.policyExtensions = clonePolicyExtensions(p)
	p.Endpoints = maps.Clone(p.Endpoints)
	p.Maintenance = slices.Clone(p.Maintenance)
	return p
}

// Validate accepts absent optional fields in older records. Zero recovery
// attempts means the existing single-attempt behavior, not unlimited attempts.
func (p Policy) Validate() error {
	if err := validatePolicyExtensions(p); err != nil {
		return err
	}
	if p.Interval <= 0 || p.Unhealthy < 0 || p.Healthy < 0 || p.Cooldown < 0 || p.RecoveryCooldown < 0 ||
		p.RecoveryMaxAttempts < 0 || p.RecoveryMaxAttempts > math.MaxInt32 {
		return errors.New("invalid durable monitor policy")
	}
	for _, count := range p.Endpoints {
		if count < 0 || count > math.MaxInt32 {
			return errors.New("invalid durable notification endpoint count")
		}
	}
	for _, window := range p.Maintenance {
		if window.Start.IsZero() || window.End.IsZero() || !window.End.After(window.Start) {
			return errors.New("invalid durable maintenance window")
		}
	}
	return nil
}

func (p Policy) InMaintenance(at time.Time) bool {
	for _, window := range p.Maintenance {
		if !at.Before(window.Start) && at.Before(window.End) {
			return true
		}
	}
	return false
}

func (p Policy) recoveryLimit() int { return max(1, p.RecoveryMaxAttempts) }

func (m Monitor) recoveryAttempts() int {
	if m.InterventionAttempted {
		// Older snapshots/logs have only the boolean and must not gain a free
		// additional attempt merely because the new counter was absent.
		return max(1, m.InterventionAttempts)
	}
	return m.InterventionAttempts
}

func (m Monitor) lastRecoveryFailure() time.Time {
	last := m.LastInterventionFailure
	for _, action := range m.Actions {
		if action.CatalogUID == m.CatalogUID && action.Kind == "intervention" && action.State == Failed && action.FinishedAt.After(last) {
			last = action.FinishedAt
		}
	}
	return last
}

func (m Monitor) mayRetryRecovery() bool {
	if !m.Policy.Intervention || m.recoveryAttempts() >= m.Policy.recoveryLimit() || m.pendingIntervention() || m.VerifyRemaining > 0 {
		return false
	}
	if m.recoveryAttempts() == 0 {
		return true
	}
	// Only a confirmed failure of the latest incident attempt permits an
	// automatic retry. Operator assertions never manufacture provider facts.
	a, ok := m.latestRecovery()
	return ok && a.State == Failed && !a.FinishedAt.IsZero()
}

func (m Monitor) recoveryDue(at time.Time) time.Time {
	if m.recoveryAttempts() <= 1 {
		return at
	}
	if failed := m.lastRecoveryFailure(); !failed.IsZero() {
		if due := failed.Add(m.Policy.RecoveryCooldown); due.After(at) {
			return due
		}
	}
	return at
}

// HasCatalog reports retained catalog identity, including tombstones and a
// bootstrap that is still pending. It is suitable for startup source selection:
// an empty active resource list must not authorize replaying an old manifest.
func (s *Store) HasCatalog() (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	s.fsm.mu.RLock()
	defer s.fsm.mu.RUnlock()
	if s.err != nil || s.fsm.err != nil {
		return false, errors.New("durable storage unavailable")
	}
	select {
	case <-s.stop:
		return false, errors.New("durable store closed")
	default:
	}
	if s.raft != nil && s.raft.State() != raft.Leader {
		return false, errors.New("durable store is not ready")
	}
	return len(s.fsm.image.Catalog) != 0 || s.fsm.image.Bootstrap != nil, nil
}
