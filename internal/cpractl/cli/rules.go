package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/ziad-hsn/cpra/sdk/go/api"
	"github.com/ziad-hsn/cpra/sdk/go/collection"
)

// Rule tests share collection normalization and the public pure matcher. They
// never initialize an API client, resolve credentials or start an executor.
func newRulesCommand(o *options) *cobra.Command {
	root := &cobra.Command{Use: "rules", Short: "Test intervention selection offline"}
	var files []string
	var settings collection.Options
	var monitor, code, phase, grpcCode, grpcStatus, expected string
	var httpStatus int
	test := &cobra.Command{Use: "test", Short: "Explain one typed observation against a monitor's rules; no action is run", Args: cobra.NoArgs}
	addCollectionInputFlags(test, &files, &settings)
	test.Flags().StringVar(&monitor, "monitor-id", "", "exact monitor ID in the supplied files")
	test.Flags().StringVar(&code, "code", "", "registered failure code")
	test.Flags().StringVar(&phase, "phase", "", "observation phase")
	test.Flags().IntVar(&httpStatus, "http-status", 0, "observed HTTP response status")
	test.Flags().StringVar(&grpcCode, "grpc-code", "", "observed gRPC transport status")
	test.Flags().StringVar(&grpcStatus, "grpc-health-status", "", "observed gRPC health status")
	test.Flags().StringVar(&expected, "expect-rule", "", "assert this matched rule ID, or @none for no match; mismatch exits unsuccessfully")
	test.RunE = func(cmd *cobra.Command, _ []string) error {
		if monitor == "" || code == "" || phase == "" {
			return errors.New("--monitor-id, --code and --phase are required")
		}
		for _, f := range files {
			if strings.Contains(f, "://") {
				return errors.New("offline rule tests accept local files, directories or stdin")
			}
		}
		frozen, err := freezeCollectionInputs(cmd, files, settings)
		if err != nil {
			return err
		}
		defer frozen.Close()
		if _, err := collection.ValidateReferences(cmd.Context(), frozen, nil); err != nil {
			return offlineRuleFailure(err)
		}
		var selected *api.MonitorSpec
		err = frozen.Range(cmd.Context(), func(item collection.Item) error {
			if err := api.ValidateResourceSettings(item.Resource); err != nil {
				return item.ValidationError(err)
			}
			if err := offlineCatalogOptions().ValidateCompiledResource(item.Resource); err != nil {
				return item.ValidationError(err)
			}
			if item.Resource.Kind != "Monitor" || item.Resource.Metadata.ID != monitor {
				return nil
			}
			var spec api.MonitorSpec
			if err := api.StrictDecode(item.Resource.Spec, &spec); err != nil {
				return err
			}
			selected = &spec
			return nil
		})
		if err != nil {
			return offlineRuleFailure(err)
		}
		if selected == nil {
			return errors.New("the selected monitor ID is absent from the supplied input")
		}
		if selected.Recovery == nil || len(selected.Recovery.Rules) == 0 {
			return errors.New("the selected monitor has no intervention rules")
		}
		details := api.FailureDiagnostic{Code: code, Phase: phase}
		if cmd.Flags().Changed("http-status") {
			details.HTTPStatus = &httpStatus
		}
		if cmd.Flags().Changed("grpc-code") {
			details.GRPCCode = &grpcCode
		}
		if cmd.Flags().Changed("grpc-health-status") {
			v := api.FailureDiagnosticGrpcStatus(grpcStatus)
			details.GRPCStatus = &v
		}
		compiled, warnings, failure, err := compileOfflineRules(cmd.Context(), frozen, *selected, details)
		if err != nil {
			return offlineRuleFailure(err)
		}

		decision := compiled.Match(failure)
		result := struct {
			MonitorID   string            `json:"monitorID"`
			Taxonomy    string            `json:"taxonomyVersion"`
			Decision    api.RuleDecision  `json:"decision"`
			Warnings    []api.RuleWarning `json:"warnings"`
			Explanation string            `json:"explanation"`
		}{monitor, selected.Recovery.TaxonomyVersion, decision, warnings, "Selection only. Runtime execution also requires fresh threshold qualification and committed admission."}
		switch o.output {
		case formatJSON, formatYAML:
			raw, _ := json.Marshal(result)
			if err := writeManagementReply(cmd, o, managementReply{data: raw}, false, false); err != nil {
				return err
			}
		default:
			match := "no intervention (no matching rule)"
			if decision.Matched {
				match = "rule " + managementDisplay(decision.RuleID)
				if !decision.Intervention {
					match += " (explicit no intervention)"
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Monitor: %s\nTaxonomy: %s\nDecision: %s\n%s\n", managementDisplay(monitor), managementDisplay(result.Taxonomy), match, result.Explanation)
			for _, warning := range warnings {
				fmt.Fprintf(cmd.OutOrStdout(), "Warning: rule %s: %s\n", managementDisplay(warning.RuleID), managementDisplay(warning.Reason))
			}
		}
		if cmd.Flags().Changed("expect-rule") && (expected == "@none" && decision.Matched || expected != "@none" && (!decision.Matched || decision.RuleID != expected)) {
			return errors.New("rule assertion failed")
		}
		return nil
	}
	root.AddCommand(test)
	return root
}

func offlineRuleFailure(err error) error {
	if err == nil {
		return nil
	}
	message := managementFailure(err).Error()
	message = strings.TrimPrefix(message, "management request failed")
	message = strings.TrimPrefix(message, "management request was rejected; inspect resource fields and version preconditions")
	return &managementCommandError{message: "offline rule validation failed" + message, cause: err}
}

func compileOfflineBuiltinRules(spec api.MonitorSpec, details api.FailureDiagnostic) (*api.CompiledRules, []api.RuleWarning, api.FailureDiagnostic, error) {
	failure, err := api.CanonicalFailure(spec.Recovery.TaxonomyVersion, details.Code, details.Phase)
	if err != nil {
		return nil, nil, failure, err
	}
	failure.HTTPStatus, failure.GRPCCode, failure.GRPCStatus = details.HTTPStatus, details.GRPCCode, details.GRPCStatus
	compiled, warnings, err := api.CompileRecoveryRules(spec.Check.Driver, *spec.Recovery)
	if err == nil {
		err = api.ValidateFailureObservation(spec.Check.Driver.Type, failure)
	}
	return compiled, warnings, failure, err
}
