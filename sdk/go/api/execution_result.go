package api

import (
	"errors"
	"strings"
)

// ValidateExecutionResult checks a bounded observation, not activation authority.
// Unknown observation vocabulary remains readable without assuming its meaning.
func ValidateExecutionResult(p Operation) error {
	return validateExecutionResult(p, true)
}

// ValidateExecutionResultMetadata validates an operation receipt containing
// availability, counts and an optional summary without an execution item page.
// Activation receipts use this shape even after the retained result is ready.
func ValidateExecutionResultMetadata(p Operation) error {
	if len(p.Items) != 0 || p.NextCursor != "" {
		return errors.New("execution metadata contains an item page")
	}
	return validateExecutionResult(p, false)
}

func validateExecutionResult(p Operation, page bool) error {
	invalid := errors.New("invalid retained execution result")
	a := p.ExecutionResult
	if a == nil {
		return nil
	}
	if !executionToken(a.State) || p.ItemCount == nil || *p.ItemCount < 1 || *p.ItemCount > 10_000_000 || !validationDigest(p.ContentDigest) || p.IdentityFormat == "" || len(p.Items) > 500 || len(p.NextCursor) > 4096 {
		return invalid
	}
	total := *p.ItemCount
	if a.Counts != nil && !executionCounts(*a.Counts, total, false) {
		return invalid
	}
	if a.Summary != nil {
		s := a.Summary
		c := ExecutionResultCounts{Processed: s.Processed, Accepted: s.Accepted, Unchanged: s.Unchanged, Conflicts: s.Conflicts, DependencyBlocked: s.DependencyBlocked, Unattempted: s.Unattempted, ChildPending: s.ChildPending, ChildApplied: s.ChildApplied, ChildFailed: s.ChildFailed, ChildSuperseded: s.ChildSuperseded, ChildInvalidated: s.ChildInvalidated}
		if s.ItemCount != total || !executionCounts(c, total, true) || !executionID(s.ResultID) || !executionID(s.UploadID) || !executionID(s.PlanID) || !validationDigest(s.PlanDigest) || !validationDigest(s.Digest) || !executionToken(s.Outcome) || s.FinalizedAt.IsZero() || !s.ExpiresAt.After(s.FinalizedAt) || s.Bytes < 0 || s.Bytes > 1<<53-1 || a.Counts != nil && *a.Counts != c {
			return invalid
		}
	}
	switch a.State {
	case "pending", "expired":
		if len(p.Items) != 0 || p.NextCursor != "" {
			return invalid
		}
	case "ready":
		if a.Summary == nil {
			return invalid
		}
	}
	var prior int64
	ids := make(map[string]bool, len(p.Items))
	for i, item := range p.Items {
		if item.InputOrdinal == nil || *item.InputOrdinal < 1 || *item.InputOrdinal > total || i > 0 && *item.InputOrdinal != prior+1 || item.PlanOrdinal == nil || *item.PlanOrdinal < 1 || *item.PlanOrdinal > total || !executionToken(item.Kind) || !strings.HasPrefix(item.ID, item.Kind+"/") || !executionID(strings.TrimPrefix(item.ID, item.Kind+"/")) || ids[item.ID] || !executionToken(item.Outcome) || !executionToken(item.CatalogDecision) || item.SourceDocument == nil || *item.SourceDocument < 1 || *item.SourceDocument > 10_000_000 || item.SourceItem == nil || *item.SourceItem < 1 || *item.SourceItem > 10_000_000 || !executionSource(item.Source) || len(item.OriginalUID) > 256 || len(item.UID) > 256 || len(item.OldVersion) > 256 || len(item.NewVersion) > 256 {
			return invalid
		}
		ids[item.ID] = true
		if item.Generation != nil && (*item.Generation < 1 || *item.Generation > 1<<53-1) || item.CommittedIndex != nil && (*item.CommittedIndex < 1 || *item.CommittedIndex > 1<<53-1) || item.DecidedAt != nil && item.DecidedAt.IsZero() {
			return invalid
		}
		child := item.ChildDisposition
		if child != nil {
			if !executionID(child.OperationID) || !executionToken(child.State) || child.Outcome != "" && !executionToken(child.Outcome) || len(child.InvalidatedByRestore) > 256 {
				return invalid
			}
			if child.State == "pending" {
				if item.Applied != nil || child.Outcome != "" || child.UpdatedAt != nil || child.InvalidatedByRestore != "" || a.State == "ready" {
					return invalid
				}
			} else if child.State == "completed" || child.State == "failed" || child.State == "partial" {
				want := map[string]string{"completed": "applied", "failed": "projection_failed", "partial": "superseded"}[child.State]
				knownOutcome := child.Outcome == "applied" || child.Outcome == "projection_failed" || child.Outcome == "superseded"
				if child.Outcome == "" || child.UpdatedAt == nil || child.UpdatedAt.IsZero() || item.Applied == nil || knownOutcome && (child.Outcome != want || *item.Applied != (child.State == "completed")) || child.InvalidatedByRestore != "" && child.State != "partial" {
					return invalid
				}
			}
		} else if item.Applied != nil {
			return invalid
		}
		switch item.CatalogDecision {
		case "accepted":
			if item.Committed == nil || !*item.Committed || child == nil || item.UID == "" || item.NewVersion == "" || item.Generation == nil || item.CommittedIndex == nil || item.DecidedAt == nil {
				return invalid
			}
		case "unchanged", "conflict", "dependencyBlocked", "unattempted":
			if item.Committed == nil || *item.Committed || child != nil || item.Applied != nil {
				return invalid
			}
			if item.CatalogDecision == "unattempted" {
				if item.DecidedAt != nil || item.CommittedIndex != nil || item.UID != "" || item.NewVersion != "" || item.Generation != nil {
					return invalid
				}
			} else if item.DecidedAt == nil || item.CommittedIndex == nil {
				return invalid
			}
			if item.CatalogDecision == "unchanged" && (item.UID == "" || item.NewVersion == "" || item.NewVersion != item.OldVersion || item.Generation == nil) {
				return invalid
			}
		}
		prior = *item.InputOrdinal
	}
	if page && a.State == "ready" && (len(p.Items) == 0 || p.NextCursor == "" && prior != total || p.NextCursor != "" && prior >= total) {
		return invalid
	}
	return nil
}
func executionCounts(c ExecutionResultCounts, total int64, terminal bool) bool {
	for _, n := range []int64{c.Processed, c.Accepted, c.Unchanged, c.Conflicts, c.DependencyBlocked, c.Unattempted, c.ChildPending, c.ChildApplied, c.ChildFailed, c.ChildSuperseded, c.ChildInvalidated} {
		if n < 0 || n > total {
			return false
		}
	}
	return c.Accepted+c.Unchanged+c.Conflicts+c.DependencyBlocked == c.Processed && c.ChildPending+c.ChildApplied+c.ChildFailed+c.ChildSuperseded+c.ChildInvalidated == c.Accepted && c.Processed+c.Unattempted <= total && (!terminal || c.ChildPending == 0 && c.Processed+c.Unattempted == total)
}
func executionToken(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func executionID(s string) bool {
	if s == "" || len(s) > 256 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.' || c == ':') {
			return false
		}
	}
	return true
}
func executionSource(s string) bool {
	if len(s) != 27 || !strings.HasPrefix(s, "source.") {
		return false
	}
	n := int64(0)
	for _, c := range s[7:] {
		if c < '0' || c > '9' {
			return false
		}
		n = n*10 + int64(c-'0')
		if n > 1_000_000 {
			return false
		}
	}
	return n > 0
}
