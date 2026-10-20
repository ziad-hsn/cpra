//go:build !externaljobs

package persistence

import (
	"bytes"
	"fmt"
	"testing"
)

func TestWorkerExpiryDefaultExclusion(t *testing.T) {
	for _, version := range []int{1, 14, 28, 29} {
		for _, value := range []string{"null", "{}"} {
			for _, kind := range []string{"barrier", "worker_expire"} {
				raw := []byte(fmt.Sprintf(`{"version":%d,"commands":[{"kind":%q,"at":"2026-09-29T00:00:00Z","worker_expire":%s}]}`, version, kind, value))
				if _, err := decodeEnvelope(raw); err == nil {
					t.Fatal("default build admitted worker expiry", version, kind)
				}
			}
		}
	}
	if supportedFormat(29) || LatestFormatVersion != ActionFormatVersion {
		t.Fatal("default storage format changed")
	}
	if _, _, err := decodeSnapshot(bytes.NewBufferString("CPRA-COLLECTION-SNAPSHOT-29\n{}"), ""); err == nil {
		t.Fatal("default accepted worker expiry snapshot")
	}
}
