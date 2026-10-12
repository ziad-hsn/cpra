//go:build !externaljobs

package persistence

import (
	"bytes"
	"fmt"
	"testing"
)

func TestWorkerCheckRetentionDefaultExclusion(t *testing.T) {
	for _, field := range []string{"worker_check_allocate", "worker_check_retire"} {
		for _, value := range []string{"null", "{}"} {
			for _, version := range []int{1, 14, 26, 27} {
				raw := []byte(fmt.Sprintf(`{"version":%d,"commands":[{"kind":"barrier","at":"2026-09-29T00:00:00Z",%q:%s}]}`, version, field, value))
				if _, err := decodeEnvelope(raw); err == nil {
					t.Fatal("default accepted worker retention command", field, version)
				}
			}
		}
	}
	if supportedFormat(27) || LatestFormatVersion != DeliveryFormatVersion {
		t.Fatal("default storage format changed")
	}
	if _, _, err := decodeSnapshot(bytes.NewBufferString("CPRA-COLLECTION-SNAPSHOT-27\n{}"), ""); err == nil {
		t.Fatal("default accepted worker retention snapshot")
	}
}
