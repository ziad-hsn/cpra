//go:build !externaljobs

package persistence

import (
	"bytes"
	"fmt"
	"reflect"
	"testing"
)

func TestWorkerSessionDefaultExclusion(t *testing.T) {
	for _, value := range []string{"null", "{}"} {
		for _, version := range []int{1, 14, 18, 19} {
			raw := []byte(fmt.Sprintf(`{"version":%d,"commands":[{"kind":"worker_poll","worker_session":%s}]}`, version, value))
			if _, err := decodeEnvelope(raw); err == nil {
				t.Fatal("default decoded worker session command")
			}
			raw = []byte(fmt.Sprintf(`{"version":%d,"index":1,"monitors":{},"slo":{},"worker_sessions":%s}`, version, value))
			if _, err := decodeImage(bytes.NewReader(raw)); err == nil {
				t.Fatal("default decoded session snapshot")
			}
		}
	}
	for _, name := range []string{"WorkerProtocolIdentity", "CommitWorkerPoll"} {
		if _, ok := reflect.TypeFor[*Store]().MethodByName(name); ok {
			t.Fatal("default exposed session API", name)
		}
	}
	if supportedFormat(19) {
		t.Fatal("default enabled worker session storage")
	}
}
