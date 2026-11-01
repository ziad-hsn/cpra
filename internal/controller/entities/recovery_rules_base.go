//go:build !externaljobs

package entities

import (
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"time"
)

func ruleInterventionBudget(target manifest.InterventionTarget) (time.Duration, error) {
	return jobs.InterventionBudget(target)
}
func compilePreparedRecoveryRules(m manifest.Monitor, mode string, rules []api.RulePredicate, targets []persistence.RecoveryRuleTarget) (persistence.RecoveryRules, error) {
	return persistence.NewRecoveryRules(m.Pulse.Type, mode, m.Intervention.TaxonomyVersion, rules, targets)
}
