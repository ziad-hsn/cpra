package api

import (
	"errors"
	"strconv"
	"strings"
)

// ValidateValidationResultPage checks a bounded read observation without granting
// activation authority. Unknown issue/change/identity-format strings remain
// readable; a mutation helper must separately recognize its supported contract.
func ValidateValidationResultPage(p ValidationResultPage) error {
	invalid := errors.New("invalid retained validation result")
	s := p.Summary
	if p.OperationID == "" || len(p.OperationID) > 128 || p.IdentityFormat == "" || len(p.IdentityFormat) > 128 || !validationDigest(p.ContentDigest) || p.ItemCount < 0 || p.ItemCount > 10_000_000 || len(p.Items) > 500 || len(p.NextCursor) > 4096 || s.ResultID == "" || len(s.ResultID) > 128 || s.Count < 0 || s.Count > 10_000 || !validationDigest(s.Digest) || !validationDigest(s.CapabilitiesDigest) || s.FinalizedAt.IsZero() || !s.ExpiresAt.After(s.FinalizedAt) || len(s.Issue) > 64 {
		return invalid
	}
	if s.SummaryOnly {
		if s.Valid || s.Issue != "validationLimit" || p.ItemCount <= 10_000 || s.Count != 0 || len(p.Items) != 0 || p.NextCursor != "" {
			return invalid
		}
	} else if s.Count != p.ItemCount || p.ItemCount > 10_000 {
		return invalid
	}
	if s.Valid {
		if s.Issue != "" || s.PlanID == "" || len(s.PlanID) > 128 || !validationDigest(s.PlanDigest) {
			return invalid
		}
	} else if s.PlanID != "" || s.PlanDigest != "" || s.Issue == "" {
		return invalid
	}
	var prior int64
	for i, item := range p.Items {
		if item.Ordinal < 1 || item.Ordinal > s.Count || i > 0 && item.Ordinal != prior+1 || item.Kind == "" || len(item.Kind) > 64 || item.ID == "" || len(item.ID) > 256 || len(item.Change) > 32 || len(item.Issue) > 64 || item.Change == "" && item.Issue == "" || (item.UID == "") != (item.ResourceVersion == "") || len(item.UID) > 256 || len(item.ResourceVersion) > 256 || item.Change == "create" && item.UID != "" || item.SourceDocument < 1 || item.SourceDocument > 10_000_000 || item.SourceItem < 1 || item.SourceItem > 10_000_000 {
			return invalid
		}
		if len(item.Source) != 27 || !strings.HasPrefix(item.Source, "source.") {
			return invalid
		}
		for _, b := range item.Source[7:] {
			if b < '0' || b > '9' {
				return invalid
			}
		}
		source, err := strconv.ParseUint(item.Source[7:], 10, 64)
		if err != nil || source < 1 || source > 1_000_000 {
			return invalid
		}
		prior = item.Ordinal
	}
	if s.Count > 0 && (len(p.Items) == 0 || p.NextCursor == "" && prior != s.Count || p.NextCursor != "" && prior >= s.Count) {
		return invalid
	}
	return nil
}

func validationDigest(v string) bool {
	if len(v) != 64 {
		return false
	}
	for _, c := range v {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
