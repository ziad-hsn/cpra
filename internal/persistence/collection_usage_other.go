//go:build !linux && !darwin && !windows

package persistence

import "context"

func (g *collectionGenerations) sampleUsage(context.Context) {
	if len(g.catalog.Completed) > 0 {
		g.usageAttempted = true
	}
}
