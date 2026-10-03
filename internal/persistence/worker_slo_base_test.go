//go:build !externaljobs

package persistence

import (
	"bytes"
	"fmt"
	"testing"
)

func TestWorkerSLODefaultExclusion(t *testing.T) {
	baseline := []byte(`{"version":1,"commands":[{"kind":"barrier","at":"2026-09-28T00:00:00Z"}]}`)
	if _, err := decodeEnvelope(baseline); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"null", "{}"} {
		injected := bytes.Replace(baseline, []byte(`"kind":"barrier"`), []byte(`"kind":"barrier","worker_observation":`+value), 1)
		if _, err := decodeEnvelope(injected); err == nil {
			t.Fatal("default ignored external observation field")
		}
		for _, version := range []int{1, 14, 20, 24, 25} {
			command := []byte(fmt.Sprintf(`{"version":%d,"commands":[{"kind":"worker_observation","worker_observation":%s}]}`, version, value))
			if _, err := decodeEnvelope(command); err == nil {
				t.Fatal("default accepted external observation command")
			}
			state := []byte(fmt.Sprintf(`{"version":%d,"index":1,"monitors":{},"slo":{},"worker_slo":%s}`, version, value))
			if _, err := decodeImage(bytes.NewReader(state)); err == nil {
				t.Fatal("default accepted external SLO image")
			}
		}
	}
	if supportedFormat(25) || LatestFormatVersion != NotificationFormatVersion {
		t.Fatal("default format changed")
	}
}
