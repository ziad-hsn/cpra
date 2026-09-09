//go:build redis

package jobs

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func TestPulseRedisJobExecute(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	job := &PulseRedisJob{
		Addr:    mr.Addr(),
		Timeout: 2 * time.Second,
		Retries: 1,
	}
	if res := job.Execute(); res.Err != nil {
		t.Fatalf("expected success against miniredis, got %v", res.Err)
	}

	bad := &PulseRedisJob{
		Addr:    "127.0.0.1:1",
		Timeout: 200 * time.Millisecond,
		Retries: 1,
	}
	if res := bad.Execute(); res.Err == nil {
		t.Fatalf("expected failure for unreachable redis server")
	}
}
