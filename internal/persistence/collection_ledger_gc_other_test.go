//go:build !linux

package persistence

import (
	"context"
	"errors"
	"testing"
)

func TestCollectionGCUnsupportedPlatformPreservesState(t *testing.T) {
	if value, err := newCollectionRetirement(t.TempDir(), nil); value != nil || !errors.Is(err, errCollectionGCUnavailable) {
		t.Fatal("unqualified native platform admitted cleanup", err)
	}
	if _, err := new(collectionRetirement).Step(context.Background(), "unused"); !errors.Is(err, errCollectionGCUnavailable) {
		t.Fatal("unqualified native platform admitted a step", err)
	}
}
