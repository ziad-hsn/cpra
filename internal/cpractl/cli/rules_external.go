//go:build externaljobs

package cli

import (
	"context"
	"errors"
	"github.com/ziad-hsn/cpra/internal/management"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

func offlineCatalogOptions() management.CatalogOptions {
	o := management.CatalogOptions{}
	o.ExternalJobsEnabled = true
	return o
}

func compileOfflineRules(ctx context.Context, frozen *collection.Frozen, spec api.MonitorSpec, details api.FailureDiagnostic) (*api.CompiledRules, []api.RuleWarning, api.FailureDiagnostic, error) {
	if spec.Check.Driver.Type != "external" {
		return compileOfflineBuiltinRules(spec, details)
	}
	var ref api.ExternalConfig
	if err := api.StrictDecode(spec.Check.Driver.Config, &ref); err != nil {
		return nil, nil, details, err
	}
	var contract *api.ExternalFailureContract
	err := frozen.Range(ctx, func(item collection.Item) error {
		if item.Resource.Kind != "JobType" || item.Resource.Metadata.ID != ref.JobTypeID {
			return nil
		}
		var jt api.JobTypeSpec
		if err := api.StrictDecode(item.Resource.Spec, &jt); err != nil {
			return err
		}
		if jt.Kind != "check" || jt.Version != ref.Version {
			return errors.New("offline JobType must match the check category and exact version")
		}
		uid := item.Resource.Metadata.UID
		if uid == "" {
			uid = "offline-uncommitted"
		}
		contract = &api.ExternalFailureContract{JobTypeID: ref.JobTypeID, JobTypeUID: uid, Version: jt.Version, ProtocolVersion: jt.ProtocolVersion, Codes: jt.FailureCodes}
		return nil
	})
	if err != nil {
		return nil, nil, details, err
	}
	if contract == nil {
		return nil, nil, details, errors.New("include the exact check JobType in the local input for an offline rule test")
	}
	predicates := make([]api.RulePredicate, len(spec.Recovery.Rules))
	for i, r := range spec.Recovery.Rules {
		predicates[i] = api.RulePredicate{ID: r.ID, Match: r.Match, None: r.None}
	}
	compiled, warnings, err := api.CompileExternalRulePredicates(*contract, spec.Recovery.TaxonomyVersion, predicates)
	if err != nil {
		return nil, nil, details, err
	}
	report := api.WorkerFailure{Code: details.Code, Phase: details.Phase, HTTPStatus: details.HTTPStatus, GRPCCode: details.GRPCCode}
	if details.GRPCStatus != nil {
		v := string(*details.GRPCStatus)
		report.GRPCStatus = &v
	}
	failure, err := api.CanonicalWorkerFailure(*contract, report)
	return compiled, warnings, failure, err
}
