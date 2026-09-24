//go:build externaljobs

package persistence

import (
	"bytes"
	"encoding/json"
	"slices"
)

// WorkerNotificationSource pins one external endpoint at its original color and
// ordinal. A zero entry represents a built-in endpoint; inline external targets
// are not covered by NotificationEndpoint worker grants.
type WorkerNotificationSource struct {
	ID       string `json:"id"`
	UID      string `json:"uid"`
	Revision string `json:"revision"`
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
			if !catalogIdentifier(source.ID, 256) || !catalogIdentifier(source.UID, 256) || !catalogIdentifier(source.Revision, 256) {
				return ErrWorkerExecutionInvalid
			}
		}
	}
	return nil
}
func policyMinimumFormat(p Policy) int {
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
