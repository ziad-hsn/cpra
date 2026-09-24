//go:build !externaljobs

package persistence

import (
	"bytes"
	"fmt"
	"reflect"
	"testing"
)

func TestWorkerPolicyDefaultExclusion(t *testing.T) {
	for _, version := range []int{1, 14, 15, 16, 17} {
		for _, value := range []string{"null", "{}", `{"mode":"bootstrap"}`} {
			wire := fmt.Sprintf(`{"version":%d,"commands":[{"kind":"recover","at":"2026-09-24T00:00:00Z","worker_policy":%s}]}`, version, value)
			if _, err := decodeEnvelope([]byte(wire)); err == nil {
				t.Fatal("default accepted policy command", version, value)
			}
			wire = fmt.Sprintf(`{"version":%d,"index":1,"monitors":{},"slo":{},"worker_policy":%s}`, version, value)
			if _, err := decodeImage(bytes.NewBufferString(wire)); err == nil {
				t.Fatal("default accepted policy snapshot", version, value)
			}
		}
	}
	for _, name := range []string{"WorkerPolicy", "CommitWorkerPolicy", "AuthenticateWorker", "CheckWorkerAuthority"} {
		if _, ok := reflect.TypeFor[*Store]().MethodByName(name); ok {
			t.Fatal("default exposed worker API", name)
		}
	}
	if _, ok := reflect.TypeFor[Command]().FieldByName("WorkerPolicy"); ok {
		t.Fatal("default exposed policy field")
	}
	if supportedFormat(17) || LatestFormatVersion != 14 {
		t.Fatal("default enabled worker format")
	}
	if _, _, err := decodeSnapshot(bytes.NewBufferString("CPRA-COLLECTION-SNAPSHOT-17\n{}"), ""); err == nil {
		t.Fatal("default accepted worker framing")
	}
}
