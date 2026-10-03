//go:build externaljobs

package persistence

import (
	"bytes"
	"encoding/json"
	"slices"
)

const WorkerNotificationFormatVersion = 28
const workerNotificationSnapshotMagic = "CPRA-COLLECTION-SNAPSHOT-28\n"

// WorkerNotificationSource pins a notification's configuration at its original
// color and ordinal. A zero entry represents a built-in notification. An omitted
// kind retains the endpoint identity used by format 20; inline sources name the
// owning Monitor explicitly and require a separate Monitor grant.
type WorkerNotificationSource struct {
	Kind     string `json:"kind,omitempty"`
	ID       string `json:"id"`
	UID      string `json:"uid"`
	Revision string `json:"revision"`
}

func (s *WorkerNotificationSource) UnmarshalJSON(raw []byte) error {
	type wire WorkerNotificationSource
	var value wire
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&value); err != nil {
		return err
	}
	var presence struct {
		Kind json.RawMessage `json:"kind"`
	}
	if err := json.Unmarshal(raw, &presence); err != nil {
		return err
	}
	if len(presence.Kind) != 0 && value.Kind != "Monitor" && value.Kind != "NotificationEndpoint" {
		return ErrWorkerExecutionInvalid
	}
	*s = WorkerNotificationSource(value)
	return nil
}

func (s WorkerNotificationSource) matches(in WorkerExecutionIntent) bool {
	kind := s.Kind
	if kind == "" {
		kind = "NotificationEndpoint"
	}
	return in.Source == (CatalogKey{Kind: kind, ID: s.ID}) && in.SourceUID == s.UID && in.SourceRevision == s.Revision
}

type policyExtensions struct {
	workerNotificationSourcesPresent bool
	WorkerNotificationSources        map[string][]WorkerNotificationSource `json:"worker_notification_sources,omitempty"`
}

func clonePolicyExtensions(p Policy) policyExtensions {
	out := policyExtensions{workerNotificationSourcesPresent: p.workerNotificationSourcesPresent}
	if p.WorkerNotificationSources != nil {
		out.WorkerNotificationSources = make(map[string][]WorkerNotificationSource, len(p.WorkerNotificationSources))
		for color, sources := range p.WorkerNotificationSources {
			out.WorkerNotificationSources[color] = slices.Clone(sources)
		}
	}
	return out
}
func validatePolicyExtensions(p Policy) error {
	if len(p.WorkerNotificationSources) > 5 {
		return ErrWorkerExecutionInvalid
	}
	total := 0
	for color, sources := range p.WorkerNotificationSources {
		if color != "red" && color != "yellow" && color != "green" && color != "cyan" && color != "gray" {
			return ErrWorkerExecutionInvalid
		}
		if len(sources) == 0 || len(sources) != p.Endpoints[color] || len(sources) > 10000 {
			return ErrWorkerExecutionInvalid
		}
		total += len(sources)
		if total > 10000 {
			return ErrWorkerExecutionInvalid
		}
		for _, source := range sources {
			if source == (WorkerNotificationSource{}) {
				continue
			}
			if source.Kind != "" && source.Kind != "Monitor" && source.Kind != "NotificationEndpoint" || !catalogIdentifier(source.ID, 256) || !catalogIdentifier(source.UID, 256) || !catalogIdentifier(source.Revision, 256) {
				return ErrWorkerExecutionInvalid
			}
		}
	}
	return nil
}
func policyMinimumFormat(p Policy) int {
	for _, sources := range p.WorkerNotificationSources {
		for _, source := range sources {
			if source.Kind != "" {
				return WorkerNotificationFormatVersion
			}
		}
	}
	if p.workerNotificationSourcesPresent || len(p.WorkerNotificationSources) != 0 {
		return WorkerExecutionFormatVersion
	}
	return FormatVersion
}
func (p *Policy) UnmarshalJSON(raw []byte) error {
	type wire Policy
	var value wire
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&value); err != nil {
		return err
	}
	if len(value.WorkerNotificationSources) == 0 {
		var presence struct {
			Sources json.RawMessage `json:"worker_notification_sources"`
		}
		if err := json.Unmarshal(raw, &presence); err != nil {
			return err
		}
		value.workerNotificationSourcesPresent = len(presence.Sources) != 0
	}
	*p = Policy(value)
	return nil
}
