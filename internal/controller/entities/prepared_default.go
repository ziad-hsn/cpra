//go:build !externaljobs

package entities

import "github.com/ziad-hsn/cpra/internal/manifest"

func validateLocalJobPreparation(manifest.Monitor, map[string]manifest.Endpoint, manifest.NotificationGroups) error {
	return nil
}
