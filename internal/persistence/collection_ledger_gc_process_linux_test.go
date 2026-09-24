package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestCollectionGCKillAfterCompletedTransition(t *testing.T) {
	if os.Getenv("CPRA_GC_KILL_TEST") == "1" {
		var protection collectionGCProtection
		if err := json.Unmarshal([]byte(os.Getenv("CPRA_GC_PROTECTION")), &protection); err != nil {
			t.Fatal(err)
		}
		f := &collectionGCFixture{dir: os.Getenv("CPRA_GC_DIRECTORY"), old: os.Getenv("CPRA_GC_CANDIDATE"), protection: protection}
		r := f.open(t)
		count, err := strconv.Atoi(os.Getenv("CPRA_GC_TRANSITIONS"))
		if err != nil || count < 1 || count > 4 {
			t.Fatal("invalid child transition count")
		}
		for i := 0; i < count; i++ {
			gcTestStep(t, r, f.old)
		}
		if err := os.WriteFile(os.Getenv("CPRA_GC_READY"), []byte("ready"), 0600); err != nil {
			t.Fatal(err)
		}
		for {
			time.Sleep(time.Hour) // Parent kills the process with native SIGKILL.
		}
	}
	for count := 1; count <= 4; count++ {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			f := newCollectionGCFixture(t)
			ready := filepath.Join(t.TempDir(), "child-ready")
			protection, err := json.Marshal(f.protection)
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestCollectionGCKillAfterCompletedTransition$")
			cmd.Env = append(os.Environ(), "CPRA_GC_KILL_TEST=1", "CPRA_GC_PROTECTION="+string(protection),
				"CPRA_GC_DIRECTORY="+f.dir, "CPRA_GC_CANDIDATE="+f.old, "CPRA_GC_TRANSITIONS="+strconv.Itoa(count), "CPRA_GC_READY="+ready)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			defer func() { _ = cmd.Process.Kill() }()
			deadline := time.Now().Add(10 * time.Second)
			for {
				if _, err := os.Stat(ready); err == nil {
					break
				}
				select {
				case err := <-done:
					t.Fatal("child exited before requested boundary", err)
				default:
				}
				if time.Now().After(deadline) {
					_ = cmd.Process.Kill()
					<-done
					t.Fatal("child did not reach requested boundary")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err == nil {
				t.Fatal("child was not forcibly terminated")
			}
			r := f.open(t)
			for i := count; i < 5; i++ {
				gcTestStep(t, r, "")
			}
			if step := gcTestStep(t, r, ""); step.Phase != "idle" {
				t.Fatal("restart failed to finish admitted retirement", step)
			}
			if _, err := os.Stat(filepath.Join(f.dir, f.old)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("admitted generation not retired after restart", err)
			}
			f.assertProtected(t)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err := r.Step(ctx, ""); !errors.Is(err, context.Canceled) {
				t.Fatal("post-recovery cancellation ignored", err)
			}
		})
	}
}
