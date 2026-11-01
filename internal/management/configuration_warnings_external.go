//go:build externaljobs

package management

import (
	"context"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// ConfigurationWarnings resolves immutable vocabulary for an advisory only.
// Admission independently authenticates the complete JobType reference graph.
func (c *Catalog) ConfigurationWarnings(ctx context.Context, resource api.Resource) []api.FieldError {
	out := api.ResourceWarnings(resource)
	var spec api.MonitorSpec
	if resource.Kind != "Monitor" || api.StrictDecode(resource.Spec, &spec) != nil || spec.Recovery == nil || len(spec.Recovery.Rules) == 0 || spec.Check.Driver.Type != "external" {
		return out
	}
	config, err := externalDriverConfig(spec.Check.Driver)
	if err != nil {
		return out
	}
	selected, exists, err := c.store.LookupJobTypeVersion(ctx, config.JobTypeID, config.Version)
	if err != nil || !exists {
		return out
	}
	predicates := make([]api.RulePredicate, 0, len(spec.Recovery.Rules))
	for _, r := range spec.Recovery.Rules {
		predicates = append(predicates, api.RulePredicate{ID: r.ID, Match: r.Match, None: r.None})
	}
	_, warnings, err := api.CompileExternalRulePredicates(selected.Version.FailureContract(), spec.Recovery.TaxonomyVersion, predicates)
	if err == nil {
		out = append(out, api.RecoveryRuleWarnings(*spec.Recovery, warnings)...)
	}
	return out
}

func (c *Catalog) validateRecoveryRuleContract(ctx context.Context, resource api.Resource) error {
	if resource.Kind != "Monitor" {
		return nil
	}
	var spec api.MonitorSpec
	if err := api.StrictDecode(resource.Spec, &spec); err != nil {
		return err
	}
	if spec.Recovery == nil || len(spec.Recovery.Rules) == 0 || spec.Check.Driver.Type != "external" {
		return nil
	}
	_, err := c.resourceJobTypeReferences(ctx, resource, true)
	return api.PrefixValidationError("spec.recovery", err)
}
