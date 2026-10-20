//go:build externaljobs

package persistence

import (
	"testing"
	"time"
)

// openWorkerMemory exercises current commands without background maintenance.
// Worker policy fixtures temporarily select administrative submission while
// provisioning identities. Production owners never change mode after Open;
// native restart and maintenance tests use real owners instead of this helper.
// Change fixture mode only between completed operations, with no concurrent
// test-owned poll, reader or caller; mode is not a runtime mutable setting.
func openWorkerMemory(t *testing.T) *Store {
	t.Helper()
	s := openReplayMemory(t)
	if err := s.migrateActionProjection(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, command := range []Command{
		{Kind: "recover", At: time.Now().UTC()},
		{Kind: "local_session", At: time.Now().UTC(), ExecutorSession: s.executorSession},
	} {
		result := submit(t, s, command)
		if len(result) != 1 || result[0].Err != nil {
			t.Fatal("worker fixture startup", result)
		}
	}
	return s
}
