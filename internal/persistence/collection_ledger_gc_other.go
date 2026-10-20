//go:build !linux && !darwin && !windows

package persistence

import (
	"context"
	"time"
)

// Native mount/identity and durable metadata removal are not yet qualified on
// these platforms. Never turn an unsupported check into permission to delete.
func newCollectionRetirement(string, collectionGCGuard) (*collectionRetirement, error) {
	return nil, errCollectionGCUnavailable
}

func (*collectionRetirement) Step(context.Context, string) (collectionGCStep, error) {
	return collectionGCStep{}, errCollectionGCUnavailable
}

func (*collectionRetirement) step(context.Context, string, time.Time) (collectionGCStep, error) {
	return collectionGCStep{}, errCollectionGCUnavailable
}
