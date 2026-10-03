//go:build !externaljobs

package persistence

import (
	"bytes"
	"fmt"
	"testing"
)

func TestWorkerRequeueDefaultExclusion(t *testing.T) {
	for _, value := range []string{"null", "{}"} {
		for _, version := range []int{1, 14, 25, 26} {
			for _, kind := range []string{"barrier", "worker_requeue"} {
				raw := []byte(fmt.Sprintf(`{"version":%d,"commands":[{"kind":%q,"at":"2026-09-28T00:00:00Z","worker_requeue":%s}]}`, version, kind, value))
				if _, err := decodeEnvelope(raw); err == nil {
					t.Fatal("default accepted tagged command", version, kind)
				}
			}
		}
	}
	if supportedFormat(26) || LatestFormatVersion != NotificationFormatVersion {
		t.Fatal("default storage format changed")
	}
	if _, _, err := decodeSnapshot(bytes.NewBufferString("CPRA-COLLECTION-SNAPSHOT-26\n{}"), ""); err == nil {
		t.Fatal("default accepted external scheduling snapshot")
	}
}
