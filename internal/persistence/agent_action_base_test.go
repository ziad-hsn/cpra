//go:build !externaljobs

package persistence

import (
	"bytes"
	"fmt"
	"testing"
)

func TestAgentStorageDefaultExcludesWorkers(t *testing.T) {
	if LatestFormatVersion != ActionFormatVersion || !supportedFormat(31) || !supportedFormat(33) || !supportedFormat(35) || !supportedFormat(38) || !supportedFormat(40) || !supportedFormat(42) || supportedFormat(41) {
		t.Fatal("missing common agent format")
	}
	for _, version := range []int{15, 20, 30, 32, 34, 36, 37, 39, 41, 43, 45, 46} {
		if supportedFormat(version) {
			t.Fatal("default build accepted optional or future format", version)
		}
		if _, _, err := decodeSnapshot(bytes.NewBufferString(fmt.Sprintf("CPRA-COLLECTION-SNAPSHOT-%d\n{}", version)), ""); err == nil {
			t.Fatal("default build opened an optional snapshot", version)
		}
	}
	for _, version := range []int{31, 33, 35, 38, 40, 42, 44} {
		for _, value := range []string{"null", "{}"} {
			data := []byte(fmt.Sprintf(`{"version":%d,"commands":[{"kind":"barrier","at":"2026-09-30T00:00:00Z","worker_result":%s}]}`, version, value))
			if _, err := decodeEnvelope(data); err == nil {
				t.Fatal("common agent format exposed worker payload", version)
			}
		}
	}
}
