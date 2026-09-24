//go:build !externaljobs

package persistence

import (
	"reflect"
	"testing"
)

func TestWorkerExecutionsReadDefaultExclusion(t *testing.T) {
	if _, ok := reflect.TypeFor[*Store]().MethodByName("WorkerExecutions"); ok {
		t.Fatal("default build exposes external execution traversal")
	}
}
