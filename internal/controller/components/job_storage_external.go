//go:build externaljobs

package components

import (
	"errors"
	"fmt"

	"github.com/ziad-hsn/cpra/internal/manifest"
)

type jobStorageExtensions struct {
	ExternalJobs *ExternalJobStorage
}

// ExternalJobStorage contains inert inputs, not local jobs or execution grants.
// Notification positions match CodeJobs exactly. Repeated destinations may share
// one private binding; Clone preserves that sharing without borrowing its input.
type ExternalJobStorage struct {
	Check         *manifest.ExternalRuntimeBinding
	Recovery      *manifest.ExternalRuntimeBinding
	Notifications map[string][]*manifest.ExternalRuntimeBinding
}

func (s *ExternalJobStorage) Clone() *ExternalJobStorage {
	if s == nil {
		return nil
	}
	seen := make(map[*manifest.ExternalRuntimeBinding]*manifest.ExternalRuntimeBinding)
	copyBinding := func(b *manifest.ExternalRuntimeBinding) *manifest.ExternalRuntimeBinding {
		if b == nil {
			return nil
		}
		if prior := seen[b]; prior != nil {
			return prior
		}
		cloned := b.Clone()
		seen[b] = &cloned
		return &cloned
	}
	cloned := &ExternalJobStorage{Check: copyBinding(s.Check), Recovery: copyBinding(s.Recovery)}
	if s.Notifications != nil {
		cloned.Notifications = make(map[string][]*manifest.ExternalRuntimeBinding, len(s.Notifications))
	}
	for color, slots := range s.Notifications {
		if slots == nil {
			cloned.Notifications[color] = nil
			continue
		}
		out := make([]*manifest.ExternalRuntimeBinding, len(slots))
		for i, b := range slots {
			out[i] = copyBinding(b)
		}
		cloned.Notifications[color] = out
	}
	return cloned
}

func (ExternalJobStorage) String() string               { return "external job storage (configuration omitted)" }
func (s ExternalJobStorage) Format(w fmt.State, _ rune) { _, _ = w.Write([]byte(s.String())) }
func (ExternalJobStorage) MarshalJSON() ([]byte, error) {
	return nil, errors.New("external job storage cannot be serialized")
}
func (ExternalJobStorage) MarshalYAML() (any, error) {
	return nil, errors.New("external job storage cannot be serialized")
}

func (e jobStorageExtensions) clone() jobStorageExtensions {
	return jobStorageExtensions{ExternalJobs: e.ExternalJobs.Clone()}
}
