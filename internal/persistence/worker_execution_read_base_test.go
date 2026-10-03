//go:build !externaljobs

package persistence

import (
	"reflect"
	"testing"
)

func TestWorkerExecutionsReadDefaultExclusion(t *testing.T) {
	for _, name := range []string{"WorkerExecutions", "WorkerResultExecution", "CommitWorkerResult", "CommitWorkerHeartbeat", "WorkerEvidenceExecution", "CommitWorkerEvidence", "FinalizeWorkerExecutions"} {
		if _, ok := reflect.TypeFor[*Store]().MethodByName(name); ok {
			t.Fatal("default build exposes external execution operation", name)
		}
	}
}
