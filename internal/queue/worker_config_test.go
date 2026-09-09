package queue

import (
	"io"
	"log"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestWorkerPoolAcceptsZeroTimeoutWithoutProcessCrash(t *testing.T) {
	if os.Getenv("CPRA_TEST_ZERO_TIMEOUT_CHILD") == "1" {
		q, _ := NewHybridQueue(HybridQueueConfig{RingCapacity: 2})
		cfg := DefaultWorkerPoolConfig()
		cfg.MinWorkers, cfg.MaxWorkers, cfg.NumShards = 1, 1, 1
		cfg.ResultBatchTimeout = 0
		p, err := NewDynamicWorkerPool(q, cfg, log.New(io.Discard, "", 0))
		if err != nil {
			return
		}
		p.Start()
		time.Sleep(30 * time.Millisecond)
		p.DrainAndStop()
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestWorkerPoolAcceptsZeroTimeoutWithoutProcessCrash$", "-test.timeout=2s")
	cmd.Env = append(os.Environ(), "CPRA_TEST_ZERO_TIMEOUT_CHILD=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("accepted worker config crashed the process: %v\n%s", err, output)
	}
}
