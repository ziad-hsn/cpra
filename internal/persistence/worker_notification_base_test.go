//go:build !externaljobs

package persistence

import (
	"bytes"
	"testing"
)

func TestWorkerNotificationDefaultExclusion(t *testing.T) {
	if supportedFormat(28) || LatestFormatVersion != DispatchFormatVersion {
		t.Fatal("default storage admitted notification extensions")
	}
	if _, _, err := decodeSnapshot(bytes.NewBufferString("CPRA-COLLECTION-SNAPSHOT-28\n{}"), ""); err == nil {
		t.Fatal("default admitted inline worker notification snapshot")
	}
}
