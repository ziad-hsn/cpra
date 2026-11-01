//go:build !externaljobs

package persistence

import "github.com/ziad-hsn/cpra/sdk/go/api"

type recoveryRuleExtensions struct{}

func compileRecoveryRuleInputs(input recoveryRuleInputs) (*api.CompiledRules, []api.RuleWarning, error) {
	return api.CompileRulePredicates(input.Driver, input.Mode, input.Taxonomy, input.Rules)
}
func validCustomFailure(FailureDiagnostic, int) bool { return false }
