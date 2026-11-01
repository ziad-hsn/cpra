package persistence

import (
	"errors"
	"math"
)

// CatalogMutationFormatVersion assigns a unique reverse-dependent token to
// every accepted catalog mutation, including mutations sharing a Raft entry.
// Formats 2 and 3 retain their original log-index semantics during replay.
const CatalogMutationFormatVersion = 4

func catalogMutationFormat(version int) bool {
	return version == CatalogMutationFormatVersion || collectionPlanStorageFormat(version)
}

var (
	ErrCatalogFormatDowngrade   = errors.New("catalog mutation requires the current durable format")
	ErrCatalogSequenceExhausted = errors.New("catalog mutation sequence exhausted")
)

func mutatesCatalog(c Command) bool {
	return c.CollectionExecute != nil && (c.CollectionExecute.Action == "begin" || c.CollectionExecute.Action == "prepare" || c.CollectionExecute.Action == "decide") || c.Catalog != nil || c.Bootstrap != nil && c.Bootstrap.Action == "seed"
}

// commandWriteFormat upgrades new catalog writes while retaining their original
// format-2/3 replay contract.
func commandWriteFormat(c Command) int {
	version := joinStorageFormat(commandMinimumFormat(c), commandExtensionWriteFormat(c))
	if c.Collection != nil && c.Collection.Action == "create" {
		version = joinStorageFormat(version, LifecycleFormatVersion)
	}
	switch c.Kind {
	case "configure", "pulse", "start", "result", "late_result", "recover", "remove", "control", "action_review", "local_action_review", "manual_recovery":
		version = joinStorageFormat(version, DispatchFormatVersion)
	}
	// Generic cleanup cannot retire execution-bearing parents. Their source
	// cleanup uses retire_sources; historical format8/9 keeps its replay semantics.
	if c.Collection != nil && c.Collection.Action == "cleanup" && c.Collection.Cleanup != nil && c.Collection.Cleanup.Activation != nil {
		version = CollectionExecutionResultFormatVersion
	}
	// Manifest checks historically carried this fence in format 1. Preserve
	// their replay contract, but mark all new fenced writes as format 2.
	if c.CheckControlRevision != "" {
		version = max(version, CatalogFormatVersion)
	}
	if mutatesCatalog(c) {
		version = max(version, CatalogMutationFormatVersion)
	}
	return version
}

// commandMinimumFormat is shared by writers and replay validation. Only the
// supported envelope versions are admitted; this does not accept future formats.
func commandMinimumFormat(c Command) int {
	if c.NotificationPermit != nil || c.NotificationFeedback != nil || c.Kind == "notification_feedback" || collectionProfileV5Command(c) {
		return joinStorageFormat(DispatchFormatVersion, commandExtensionMinimumFormat(c))
	}
	if c.Config != nil && (c.Config.ActionVersion == DispatchFormatVersion || len(c.Config.Policy.NotificationBindings) > 0) {
		return joinStorageFormat(DispatchFormatVersion, commandExtensionMinimumFormat(c))
	}
	if c.ManualRecovery != nil && (c.ManualRecovery.RuleID != "" || c.ManualRecovery.ExpectedDecision != "" || c.ManualRecovery.ExpectedConfiguration != "") {
		return joinStorageFormat(RecoveryRulesFormatVersion, commandExtensionMinimumFormat(c))
	}
	if c.Failure.TaxonomyVersion != "" || c.Failure.Phase != "" || c.Config != nil && requiresRecoveryRules(*c.Config) || collectionProfileV4Command(c) {
		return joinStorageFormat(RecoveryRulesFormatVersion, commandExtensionMinimumFormat(c))
	}
	if c.Kind == "action_project" && c.Revision == "notifications" {
		return joinStorageFormat(NotificationConditionFormatVersion, commandExtensionMinimumFormat(c))
	}
	if c.Failure.requiresConditionFormat() {
		return joinStorageFormat(NotificationConditionFormatVersion, commandExtensionMinimumFormat(c))
	}
	if c.Kind == "action_retire" || c.ActionRetirement != nil {
		return joinStorageFormat(NotificationConditionFormatVersion, commandExtensionMinimumFormat(c))
	}
	if c.Config != nil && requiresNotificationConditions(*c.Config) {
		return joinStorageFormat(NotificationConditionFormatVersion, commandExtensionMinimumFormat(c))
	}
	if c.Kind == "action_reconcile" || c.Kind == "action_project" {
		return ActionFormatVersion
	}
	if collectionProfileV3Command(c) {
		return joinStorageFormat(ActionFormatVersion, commandExtensionMinimumFormat(c))
	}
	if c.Outcome == "configuration_problem" || c.Failure.configurationProblem() {
		return joinStorageFormat(ActionFormatVersion, commandExtensionMinimumFormat(c))
	}
	if c.Kind == "installation" || c.InstallationID != "" {
		return joinStorageFormat(ActionFormatVersion, commandExtensionMinimumFormat(c))
	}
	if c.NoticeRevision != "" {
		return joinStorageFormat(ActionFormatVersion, commandExtensionMinimumFormat(c))
	}
	if c.Config != nil && requiresActionFormat(*c.Config) {
		return joinStorageFormat(ActionFormatVersion, commandExtensionMinimumFormat(c))
	}
	if collectionProfileV2Command(c) || collectionFieldDiagnosticsCommand(c) {
		return joinStorageFormat(LifecycleFormatVersion, commandExtensionMinimumFormat(c))
	}
	if c.Failure.requiresLifecycleFormat() {
		return joinStorageFormat(LifecycleFormatVersion, commandExtensionMinimumFormat(c))
	}
	if c.Control != nil && (c.Control.Action == "ignore" || c.Control.Action == "unignore") {
		return joinStorageFormat(LifecycleFormatVersion, commandExtensionMinimumFormat(c))
	}
	if c.Kind == "local_action_review" {
		return LifecycleFormatVersion
	}
	if c.Config != nil && requiresLifecycleFormat(*c.Config) {
		return joinStorageFormat(LifecycleFormatVersion, commandExtensionMinimumFormat(c))
	}
	if c.RetryDelay != 0 || c.Failure != (FailureDiagnostic{}) || !c.ActionDeadline.IsZero() || c.Config != nil && requiresDeliveryFormat(*c.Config) {
		return joinStorageFormat(DeliveryFormatVersion, commandExtensionMinimumFormat(c))
	}
	if c.AgentConnection != nil || c.AgentObservation != nil || c.Kind == "agent_observation" {
		return joinStorageFormat(AgentObservationFormatVersion, commandExtensionMinimumFormat(c))
	}
	if c.Config != nil && recoveryNotificationPolicyFormat(c.Config.Policy) != FormatVersion {
		return joinStorageFormat(RecoveryNotificationFormatVersion, commandExtensionMinimumFormat(c))
	}
	if c.AgentReceipt != nil || c.Kind == "agent_receipt" || c.Kind == "start" && c.Driver == "a2a" {
		return AgentActionFormatVersion
	}
	if version := commandExtensionMinimumFormat(c); version != FormatVersion {
		return version
	}
	if collectionReselectionCommand(c) {
		return CollectionReselectionFormatVersion
	}
	if c.CollectionExecute != nil {
		if c.CollectionExecute.Action == "retire_sources" || c.CollectionExecute.SourceRetirement != nil || c.CollectionExecute.Retirement != nil && c.CollectionExecute.Retirement.Retirement != nil && c.CollectionExecute.Retirement.Retirement.Sources != nil {
			return CollectionExecutionSourceRetirementFormatVersion
		}
		if c.CollectionExecute.Action == "retire" || c.CollectionExecute.Retirement != nil {
			return CollectionExecutionRetirementFormatVersion
		}
		if c.CollectionExecute.Action == "publish" {
			return CollectionExecutionPublicationFormatVersion
		}
		if c.CollectionExecute.Action == "finalize" {
			return CollectionExecutionResultFormatVersion
		}
		return CollectionExecutionFormatVersion
	}
	if c.Authentication != nil && c.Authentication.Version == AuthenticationLifecycleFormatVersion {
		return CollectionValidationFormatVersion
	}
	if c.Collection != nil {
		if collectionActivationCommand(*c.Collection) {
			return CollectionActivationFormatVersion
		}
		if collectionValidationRequestCommand(*c.Collection) {
			return CollectionValidationRequestFormatVersion
		}
		if collectionValidationCommand(*c.Collection) || c.Collection.Create != nil && c.Collection.Create.Owner != nil {
			return CollectionValidationFormatVersion
		}
		if collectionPlanCommand(*c.Collection) {
			return CollectionPlanFormatVersion
		}
		return CollectionFormatVersion
	}
	if c.OperationAllocation != nil || c.Kind == "operation_expire" || c.Authentication != nil || c.Restore != nil || c.Operation != nil || c.Catalog != nil || c.Guard != nil || c.Bootstrap != nil || c.Control != nil || c.ManualRecovery != nil || c.ActionReview != nil || c.ExecutorSession != "" {
		return CatalogFormatVersion
	}
	return FormatVersion
}

// nextCatalogMutationSequence does not mutate state. Call after preconditions,
// before the first mutation. The previous image index bounds every old token,
// so the first new sequence exceeds all legacy values without a fleet rewrite.
func (f *machine) nextCatalogMutationSequence(version int) (uint64, error) {
	if !catalogMutationFormat(version) {
		if catalogMutationFormat(f.image.Version) || f.image.CatalogMutationSequence != 0 {
			return 0, ErrCatalogFormatDowngrade
		}
		return 0, nil
	}
	previous := f.image.CatalogMutationSequence
	if previous == 0 {
		previous = f.image.Index
	}
	if previous == math.MaxUint64 {
		return 0, ErrCatalogSequenceExhausted
	}
	return previous + 1, nil
}

func collectionFieldDiagnosticsCommand(c Command) bool {
	if c.Collection != nil {
		for _, item := range c.Collection.ValidationItems {
			if item.Field != "" || item.Reason != "" {
				return true
			}
		}
	}
	return false
}
