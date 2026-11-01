package manifest

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/ziad-hsn/cpra/sdk/go/api"
	"gopkg.in/yaml.v3"
)

// InterventionRule keeps file action syntax while using the shared typed matcher.
type InterventionRule struct {
	ID     string             `yaml:"id" json:"id"`
	Match  FailureMatch       `yaml:"match" json:"match"`
	None   bool               `yaml:"none,omitempty" json:"none,omitempty"`
	Action string             `yaml:"action,omitempty" json:"action,omitempty"`
	Target InterventionTarget `yaml:"target,omitempty" json:"target,omitempty"`
}

type FailureMatch struct {
	Codes        []string `yaml:"codes" json:"codes"`
	Phases       []string `yaml:"phases,omitempty" json:"phases,omitzero"`
	HTTPStatuses []int    `yaml:"http_statuses,omitempty" json:"http_statuses,omitzero"`
	GRPCCodes    []string `yaml:"grpc_codes,omitempty" json:"grpc_codes,omitzero"`
	GRPCStatuses []string `yaml:"grpc_statuses,omitempty" json:"grpc_statuses,omitzero"`
}

func CloneInterventionRules(rules []InterventionRule) []InterventionRule {
	result := slices.Clone(rules)
	for i := range result {
		m := &result[i].Match
		m.Codes, m.Phases = slices.Clone(m.Codes), slices.Clone(m.Phases)
		m.HTTPStatuses, m.GRPCCodes, m.GRPCStatuses = slices.Clone(m.HTTPStatuses), slices.Clone(m.GRPCCodes), slices.Clone(m.GRPCStatuses)
		if result[i].Target != nil {
			result[i].Target = result[i].Target.Copy()
		}
	}
	return result
}

func FailureMatchFromAPI(m api.FailureMatch) FailureMatch {
	result := FailureMatch{Codes: slices.Clone(m.Codes), Phases: slices.Clone(m.Phases), HTTPStatuses: slices.Clone(m.HTTPStatuses), GRPCCodes: slices.Clone(m.GRPCCodes)}
	if m.GRPCStatuses != nil {
		result.GRPCStatuses = make([]string, len(m.GRPCStatuses))
		for i, v := range m.GRPCStatuses {
			result.GRPCStatuses[i] = string(v)
		}
	}
	return result
}

func (i Intervention) MarshalJSON() ([]byte, error) {
	type plain Intervention
	if i.Rules == nil && i.TaxonomyVersion == "" {
		return json.Marshal(plain(i))
	}
	if i.Action != "" || i.Target != nil {
		return nil, api.InvalidField("intervention", "recoveryChoice")
	}
	return json.Marshal(struct {
		TaxonomyVersion string             `json:"taxonomy_version"`
		Rules           []InterventionRule `json:"rules"`
		Retries         int                `json:"retries"`
		MaxFailures     int                `json:"max_failures"`
		MaxAttempts     int                `json:"max_attempts,omitempty"`
	}{i.TaxonomyVersion, i.Rules, i.Retries, i.MaxFailures, i.MaxAttempts})
}

func (m FailureMatch) API() api.FailureMatch {
	var statuses []api.FailureMatchGrpcStatuses
	if m.GRPCStatuses != nil {
		statuses = make([]api.FailureMatchGrpcStatuses, len(m.GRPCStatuses))
	}
	for i, value := range m.GRPCStatuses {
		statuses[i] = api.FailureMatchGrpcStatuses(value)
	}
	return api.FailureMatch{Codes: m.Codes, Phases: m.Phases, HTTPStatuses: m.HTTPStatuses, GRPCCodes: m.GRPCCodes, GRPCStatuses: statuses}
}

func (r *InterventionRule) UnmarshalYAML(node *yaml.Node) error {
	var input struct {
		ID     string       `yaml:"id"`
		Match  FailureMatch `yaml:"match"`
		None   bool         `yaml:"none"`
		Action string       `yaml:"action"`
		Target yaml.Node    `yaml:"target"`
	}
	if err := node.Decode(&input); err != nil {
		return err
	}
	*r = InterventionRule{ID: input.ID, Match: input.Match, None: input.None, Action: input.Action}
	if input.Action != "" {
		// Reuse the existing target decoder and its duration/type validation.
		var intervention Intervention
		action := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: input.Action}
		value := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{{Kind: yaml.ScalarNode, Value: "action"}, action, {Kind: yaml.ScalarNode, Value: "target"}, &input.Target}}
		if err := value.Decode(&intervention); err != nil {
			return err
		}
		r.Target = intervention.Target
	} else if input.Target.Kind != 0 {
		return api.InvalidField("target", "ruleDecision")
	}
	return nil
}

func (r *InterventionRule) UnmarshalJSON(raw []byte) error {
	var input struct {
		ID     string          `json:"id"`
		Match  FailureMatch    `json:"match"`
		None   bool            `json:"none"`
		Action string          `json:"action"`
		Target json.RawMessage `json:"target"`
	}
	if err := api.StrictDecode(raw, &input); err != nil {
		return err
	}
	*r = InterventionRule{ID: input.ID, Match: input.Match, None: input.None, Action: input.Action}
	if input.Action != "" {
		data, _ := json.Marshal(map[string]any{"action": input.Action, "target": input.Target})
		var intervention Intervention
		if err := json.Unmarshal(data, &intervention); err != nil {
			return err
		}
		r.Target = intervention.Target
	} else if input.Target != nil {
		return api.InvalidField("target", "ruleDecision")
	}
	return nil
}

// RecoveryRules normalizes inert matcher/action settings for shared validation.
// Callers must retain the manifest's original location for any returned field error.
func (i Intervention) RecoveryRules() (api.RecoverySpec, error) {
	result := api.RecoverySpec{TaxonomyVersion: i.TaxonomyVersion, Rules: make([]api.RecoveryRule, len(i.Rules))}
	for index, rule := range i.Rules {
		value := api.RecoveryRule{ID: rule.ID, Match: rule.Match.API(), None: rule.None}
		if rule.Action != "" {
			raw, err := normalizedDriverSettings("recovery", rule.Action, rule.Target)
			if err != nil {
				return result, api.PrefixValidationError(fmt.Sprintf("rules[%d].driver", index), err)
			}
			value.Driver = &api.DriverConfig{Type: rule.Action, Config: raw}
		}
		result.Rules[index] = value
	}
	if i.Action != "" {
		result.Driver.Type = i.Action
	}
	return result, nil
}
