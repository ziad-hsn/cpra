package entities

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/mlange-42/ark/ecs"
	"github.com/ziad-hsn/cpra/internal/controller/components"
	"github.com/ziad-hsn/cpra/internal/jobs"
	"github.com/ziad-hsn/cpra/internal/manifest"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

func prepareRecoveryRules(m manifest.Monitor, revision string) (persistence.RecoveryRules, error) {
	var rules []api.RulePredicate
	var targets []persistence.RecoveryRuleTarget
	for _, rule := range m.Intervention.Rules {
		rules = append(rules, api.RulePredicate{ID: rule.ID, Match: rule.Match.API(), None: rule.None})
		if rule.None {
			continue
		}
		budget, err := ruleInterventionBudget(rule.Target)
		if err != nil {
			return persistence.RecoveryRules{}, err
		}
		digest := sha256.Sum256([]byte("recovery-rule/" + revision + "/" + rule.ID))
		targets = append(targets, persistence.RecoveryRuleTarget{RuleID: rule.ID, Driver: rule.Action, Binding: hex.EncodeToString(digest[:]), Budget: budget})
	}
	mode := ""
	if m.Pulse.Type == "grpc" {
		var config struct {
			Mode string `json:"mode"`
		}
		data, _ := json.Marshal(m.Pulse.Config)
		_ = json.Unmarshal(data, &config)
		mode = config.Mode
	}
	return compilePreparedRecoveryRules(m, mode, rules, targets)
}

func (p *PreparedMonitor) RecoveryRules() persistence.RecoveryRules {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.bundle == nil || p.bundle.intervention == nil {
		return persistence.RecoveryRules{}
	}
	return p.bundle.intervention.Rules
}

// PrepareRuleJob constructs only the committed selection. Runtime configuration
// is privately owned by the controller; the durable start guard remains required.
func PrepareRuleJob(config *components.InterventionConfig, selection persistence.RecoverySelection, entity ecs.Entity) (jobs.Job, components.ActionFlow, error) {
	if config == nil || config.Rules.Digest() != selection.PolicyDigest {
		return nil, components.ActionFlow{}, persistence.ErrRecoveryRules
	}
	target, ok := config.Rules.Target(selection.RuleID)
	if !ok || target != selection.Target {
		return nil, components.ActionFlow{}, persistence.ErrRecoveryRules
	}
	for _, rule := range config.Choices {
		if rule.ID != selection.RuleID {
			continue
		}
		if rule.None || rule.Target == nil || rule.Action != target.Driver {
			break
		}
		job, err := jobs.CreateInterventionJob(manifest.Intervention{Action: rule.Action, Target: rule.Target}, entity)
		if err != nil {
			return nil, components.ActionFlow{}, err
		}
		flow, err := actionDestination(rule.Action, rule.Target)
		if err != nil {
			return nil, components.ActionFlow{}, err
		}
		return &entityJob{Job: job, entity: entity}, flow, nil
	}
	return nil, components.ActionFlow{}, errors.New("committed recovery rule has no prepared action")
}
