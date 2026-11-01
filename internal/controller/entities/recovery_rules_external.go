//go:build externaljobs

package entities

import (
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"time"
)

func ruleInterventionBudget(target manifest.InterventionTarget) (time.Duration, error) {
	if marker, ok := target.(*manifest.ExternalInterventionConfig); ok {
		if marker == nil || marker.Budget <= 0 || marker.Budget > 24*time.Hour {
			return 0, errExternalPreparation
		}
		return marker.Budget, nil
	}
	return jobs.InterventionBudget(target)
}
func compilePreparedRecoveryRules(m manifest.Monitor, mode string, rules []api.RulePredicate, targets []persistence.RecoveryRuleTarget) (persistence.RecoveryRules, error) {
	if m.Pulse.Type == "external" {
		marker, ok := m.Pulse.Config.(*manifest.ExternalPulseConfig)
		if !ok || marker == nil || marker.FailureContract == nil {
			return persistence.RecoveryRules{}, errExternalPreparation
		}
		c := marker.FailureContract
		if c.JobTypeID != marker.Key.JobType.JobTypeID || c.JobTypeUID != marker.Key.JobType.JobTypeUID || c.Version != marker.Key.JobType.Version {
			return persistence.RecoveryRules{}, errExternalPreparation
		}
		return persistence.NewExternalRecoveryRules(*c, m.Intervention.TaxonomyVersion, rules, targets)
	}
	return persistence.NewRecoveryRules(m.Pulse.Type, mode, m.Intervention.TaxonomyVersion, rules, targets)
}
