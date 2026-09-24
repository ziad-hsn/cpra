//go:build externaljobs

package worker

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ziad-hsn/cpra/sdk/go/api"
)

// These child processes execute the real runner/journal against an explicitly
// local protocol fixture. They prove crash boundaries, not server interoperation.
func TestSessionProcessHelper(t *testing.T) {
	phase := os.Getenv("CPRA_WORKER_SESSION_PHASE")
	if phase == "" {
		return
	}
	p := &protocolFuncs{fakeProtocol: newFake()}
	c := Config{Client: p, Registry: NewRegistry(), WorkerID: "worker-1", WorkerUID: "worker-uid-1", ServerID: "server-epoch-1", StateDir: os.Getenv("CPRA_WORKER_STATE"), WrappingKeyPath: os.Getenv("CPRA_WORKER_KEY"), RetryInterval: time.Millisecond}
	stopAtBoundary := func() { fmt.Println("session-boundary-ready"); select {} }
	p.start = func(_ context.Context, q api.StartRequest) (*api.StartResponse, error) {
		if q.Mode != api.StartModeBegin {
			panic("unexpected reconcile in live child")
		}
		if phase == "begin-reply" {
			stopAtBoundary()
		}
		return startReply(q, api.StartDispositionGranted), nil
	}
	p.result = func(_ context.Context, out api.Outcome) (*api.Receipt, error) {
		if phase == "outcome" {
			stopAtBoundary()
		}
		if err := os.WriteFile(filepath.Join(filepath.Dir(c.StateDir), "committed-receipt"), []byte(out.ExecutionID), 0600); err != nil {
			panic(err)
		}
		if phase == "receipt-reply" {
			stopAtBoundary()
		}
		return &api.Receipt{ServerID: out.ServerID, WorkerUID: out.WorkerUID, ExecutionID: out.ExecutionID, ReceiptID: "process-receipt", Disposition: api.ReceiptDispositionFinalized}, nil
	}
	if err := c.Registry.Register("example", "v1", "recovery", func(context.Context, Job) (api.Outcome, error) {
		if phase == "granted" {
			stopAtBoundary()
		}
		if err := os.WriteFile(filepath.Join(filepath.Dir(c.StateDir), "effect"), []byte("one fixture effect"), 0600); err != nil {
			panic(err)
		}
		return api.Outcome{Status: "completed"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	r, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	p.assignments <- assignment("session-process", "recovery")
	if phase == "receipt-removed" {
		go func() {
			for {
				if _, err := os.Stat(filepath.Join(filepath.Dir(c.StateDir), "committed-receipt")); err == nil {
					s, err := r.Status()
					if err == nil && s.Records == 0 {
						stopAtBoundary()
					}
				}
				time.Sleep(time.Millisecond)
			}
		}()
	}
	if err = r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSessionForcedProcessTermination(t *testing.T) {
	for _, phase := range []string{"begin-reply", "granted", "outcome", "receipt-reply", "receipt-removed"} {
		t.Run(phase, func(t *testing.T) {
			c := testConfig(t)
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(binary, "-test.run=^TestSessionProcessHelper$")
			cmd.Env = append(os.Environ(), "CPRA_WORKER_SESSION_PHASE="+phase, "CPRA_WORKER_STATE="+c.StateDir, "CPRA_WORKER_KEY="+c.WrappingKeyPath)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			joined := false
			t.Cleanup(func() {
				if !joined {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			})
			ready := make(chan bool, 1)
			go func() {
				scanner := bufio.NewScanner(stdout)
				ready <- scanner.Scan() && scanner.Text() == "session-boundary-ready"
			}()
			select {
			case ok := <-ready:
				if !ok {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
					joined = true
					t.Fatalf("child failed: %s", stderr.String())
				}
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				joined = true
				t.Fatalf("child timeout: %s", stderr.String())
			}
			if err = cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = cmd.Wait()
			joined = true
			if bytes.Contains(stderr.Bytes(), []byte("WARNING: DATA RACE")) {
				t.Fatalf("child race: %s", stderr.String())
			}
			c, _ = c.validate()
			j, err := openJournal(c)
			if err != nil {
				t.Fatal(err)
			}
			records, err := j.records()
			if err != nil {
				_ = j.close()
				t.Fatal(err)
			}
			_ = j.close()
			if phase == "receipt-removed" {
				if len(records) != 0 {
					t.Fatal("acknowledged record remained")
				}
				return
			}
			if len(records) != 1 {
				t.Fatalf("lost original journal entry: %d", len(records))
			}
			want := "outcome"
			if phase == "begin-reply" {
				want = "reserved"
			}
			if phase == "granted" {
				want = "started"
			}
			if records[0].State != want || records[0].Start.Mode != api.StartModeReconcile || records[0].Start.SessionID != "session-1" || records[0].WorkerUID != c.WorkerUID {
				t.Fatalf("wrong recovery identity/state: %+v", records[0])
			}
			p := &protocolFuncs{fakeProtocol: newFake()}
			c.Client = p
			var invocations atomic.Int64
			_ = c.Registry.Register("example", "v1", "recovery", func(context.Context, Job) (api.Outcome, error) { invocations.Add(1); return api.Outcome{}, nil })
			p.start = func(_ context.Context, q api.StartRequest) (*api.StartResponse, error) {
				if q.Mode != api.StartModeReconcile {
					t.Error("replayed begin after crash")
				}
				return startReply(q, api.StartDispositionStarted), nil
			}
			r, _, _ := runWorker(t, c)
			eventually(t, func() bool {
				s, _ := r.Status()
				if want == "outcome" {
					return s.Records == 0
				}
				return s.UnknownActions == 1
			})
			if invocations.Load() != 0 {
				t.Fatal("process recovery invoked handler")
			}
			p.mu.Lock()
			results := append([]api.Outcome(nil), p.results...)
			p.mu.Unlock()
			if len(results) == 0 || results[0].ServerID != c.ServerID || results[0].WorkerUID != c.WorkerUID {
				t.Fatal("missing pinned result")
			}
			if phase == "receipt-reply" {
				if _, err = os.Stat(filepath.Join(filepath.Dir(c.StateDir), "committed-receipt")); err != nil {
					t.Fatal("receipt fixture did not commit before kill")
				}
				if results[0].Status != "completed" {
					t.Fatal("lost receipt changed confirmed outcome")
				}
			}
		})
	}
}
