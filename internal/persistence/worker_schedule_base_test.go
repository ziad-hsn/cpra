//go:build !externaljobs

package persistence

import (
	"bytes"
	"fmt"
	"testing"
)

func TestWorkerScheduleDefaultExclusion(t *testing.T) {
	for _, version := range []int{1, 14, 29, 30} {
		for _, value := range []string{"null", "{}"} {
			for _, kind := range []string{"barrier", "worker_schedule"} {
				raw := []byte(fmt.Sprintf(`{"version":%d,"commands":[{"kind":%q,"at":"2026-09-29T00:00:00Z","worker_schedule":%s}]}`, version, kind, value))
				if _, err := decodeEnvelope(raw); err == nil {
					t.Fatal("default admitted worker schedules")
				}
			}
		}
	}
	if supportedFormat(30) || LatestFormatVersion != DispatchFormatVersion {
		t.Fatal("default format changed")
	}
	if _, _, err := decodeSnapshot(bytes.NewBufferString("CPRA-COLLECTION-SNAPSHOT-30\n{}"), ""); err == nil {
		t.Fatal("default accepted schedule snapshot")
	}
}
