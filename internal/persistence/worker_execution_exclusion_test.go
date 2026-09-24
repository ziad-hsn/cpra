//go:build !externaljobs

package persistence

import (
	"bytes"
	"fmt"
	"reflect"
	"testing"
)

func TestWorkerExecutionDefaultExclusion(t *testing.T) {
	baseline := []byte(`{"version":1,"commands":[{"kind":"barrier","at":"2026-09-24T12:00:00Z"}]}`)
	if _, err := decodeEnvelope(baseline); err != nil {
		t.Fatal("valid barrier fixture rejected", err)
	}
	for _, value := range []string{"null", "{}"} {
		injected := bytes.Replace(baseline, []byte(`"kind":"barrier"`), []byte(`"kind":"barrier","worker_execution":`+value), 1)
		if _, err := decodeEnvelope(injected); err == nil {
			t.Fatal("default ignored optional execution field on valid command")
		}
	}
	for _, value := range []string{"null", "{}"} {
		for _, version := range []int{1, 14, 19, 20} {
			raw := []byte(fmt.Sprintf(`{"version":%d,"commands":[{"kind":"worker_execution","worker_execution":%s}]}`, version, value))
			if _, err := decodeEnvelope(raw); err == nil {
				t.Fatal("default interpreted execution command")
			}
			raw = []byte(fmt.Sprintf(`{"version":%d,"index":1,"monitors":{},"slo":{},"worker_executions":%s}`, version, value))
			if _, err := decodeImage(bytes.NewReader(raw)); err == nil {
				t.Fatal("default interpreted execution namespace")
			}
		}
	}
	for _, name := range []string{"CommitWorkerExecution", "WorkerExecution", "WorkerExecutionsReady"} {
		if _, ok := reflect.TypeFor[*Store]().MethodByName(name); ok {
			t.Fatal("default exposed execution API", name)
		}
	}
	if _, ok := reflect.TypeFor[Policy]().FieldByName("WorkerNotificationSources"); ok {
		t.Fatal("default exposed notification execution mapping")
	}
	if supportedFormat(20) {
		t.Fatal("default interpreted execution format")
	}
}
