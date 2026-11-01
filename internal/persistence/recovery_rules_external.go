//go:build externaljobs

package persistence

import (
	"encoding/json"
	"github.com/ziad-hsn/cpra/sdk/go/api"
)

type recoveryRuleExtensions struct {
	ExternalFailure *api.ExternalFailureContract `json:"external_failure,omitempty"`
}

func compileRecoveryRuleInputs(input recoveryRuleInputs) (*api.CompiledRules, []api.RuleWarning, error) {
	if input.Driver == "external" {
		if input.ExternalFailure == nil {
			return nil, nil, ErrRecoveryRules
		}
		return api.CompileExternalRulePredicates(*input.ExternalFailure, input.Taxonomy, input.Rules)
	}
	if input.ExternalFailure != nil {
		return nil, nil, ErrRecoveryRules
	}
	return api.CompileRulePredicates(input.Driver, input.Mode, input.Taxonomy, input.Rules)
}

func NewExternalRecoveryRules(contract api.ExternalFailureContract, taxonomy string, rules []api.RulePredicate, targets []RecoveryRuleTarget) (RecoveryRules, error) {
	raw, err := json.Marshal(recoveryRuleInputs{Driver: "external", Taxonomy: taxonomy, Rules: rules, Targets: targets, recoveryRuleExtensions: recoveryRuleExtensions{ExternalFailure: &contract}})
	if err != nil {
		return RecoveryRules{}, err
	}
	var result RecoveryRules
	err = json.Unmarshal(raw, &result)
	return result, err
}

func validCustomFailure(d FailureDiagnostic, version int) bool {
	return (version == WorkerRecoveryRulesFormatVersion || version == WorkerDispatchFormatVersion) && d.TaxonomyVersion == api.FailureTaxonomyVersion && api.ValidCustomFailureCode(d.Code) && d.Phase == "check" && d.Message == "External check reported a declared failure" && d.HTTPStatus == 0 && d.GRPCCode == "" && d.GRPCStatus == "" && d.CertificateNotAfter.IsZero() && d.CertificateDaysRemaining == 0
}

func (v JobTypeVersion) FailureContract() api.ExternalFailureContract {
	return api.ExternalFailureContract{JobTypeID: v.Record.Key.ID, JobTypeUID: v.Record.UID, Version: v.Version, ProtocolVersion: v.ProtocolVersion, Codes: append([]string(nil), v.FailureCodes...)}
}

func effectiveWorkerProtocol(version string) string {
	if version == "" {
		return "1"
	}
	return version
}

func validateStructuredWorkerResult(i image, r WorkerExecutionRecord, input WorkerResultInput, format int) error {
	if input.Failure == (FailureDiagnostic{}) {
		return nil
	}
	if (format != WorkerRecoveryRulesFormatVersion && format != WorkerDispatchFormatVersion) || r.Intent.Category != "check" || i.JobTypes == nil {
		return ErrWorkerExecutionInvalid
	}
	ref := r.Intent.JobType
	typ, ok := i.JobTypes.Records[ref.JobTypeID].Versions[ref.Version]
	if !ok || typ.Record.UID != ref.JobTypeUID || typ.Record.Revision != ref.Revision || typ.ProtocolVersion != "2" {
		return ErrWorkerExecutionInvalid
	}
	f := input.Failure.Public()
	report := api.WorkerFailure{Code: f.Code, Phase: f.Phase, HTTPStatus: f.HTTPStatus, GRPCCode: f.GRPCCode, CertificateNotAfter: f.CertificateNotAfter, CertificateDaysRemaining: f.CertificateDaysRemaining}
	if f.GRPCStatus != nil {
		v := string(*f.GRPCStatus)
		report.GRPCStatus = &v
	}
	canonical, err := api.CanonicalWorkerFailure(typ.FailureContract(), report)
	if err != nil || CheckFailure(canonical) != input.Failure || (input.Status == "noData") != input.Failure.MonitoringUnavailable() {
		return ErrWorkerExecutionInvalid
	}
	return nil
}
