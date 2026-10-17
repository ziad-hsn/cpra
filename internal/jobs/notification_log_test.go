package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestNotificationLogEmptyFileUsesOpenStdout(t *testing.T) {
	if result := (&CodeLogJob{Monitor: "stdout-regression", Color: "gray"}).Execute(); result.Err != nil {
		t.Fatal(result.Err)
	}
	if _, err := os.Stdout.Stat(); err != nil {
		t.Fatal("notification closed process stdout", err)
	}
}

func TestNotificationLogConcurrentFirstWrites(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "notifications")
	log, err := OpenNotificationLog(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	const count = 32
	start := make(chan struct{})
	results := make(chan error, count)
	var workers sync.WaitGroup
	for range count {
		workers.Go(func() {
			<-start
			results <- (&CodeLogJob{Log: log, File: "team/service/events.jsonl", Monitor: "test", Color: "red"}).Execute().Err
		})
	}
	close(start)
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal("concurrent first write failed", err)
		}
	}
	contents, err := os.ReadFile(filepath.Join(directory, "team", "service", "events.jsonl"))
	if err != nil || strings.Count(string(contents), "\n") != count {
		t.Fatal("lost notification", err)
	}
}

func TestNotificationLogRootConfinement(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "notifications")
	log, err := OpenNotificationLog(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	job := &CodeLogJob{Log: log, File: "team/alerts.jsonl", Monitor: "test", Color: "red"}
	if result := job.Execute(); result.Err != nil {
		t.Fatal("rooted append failed", result.Err)
	}
	if result := job.Copy().Execute(); result.Err != nil {
		t.Fatal("copy lost root", result.Err)
	}
	contents, err := os.ReadFile(filepath.Join(directory, "team", "alerts.jsonl"))
	if err != nil || strings.Count(string(contents), "\n") != 2 {
		t.Fatal("append lost notification", err)
	}
	for _, name := range []string{"../canary", "/canary", "team/../../canary", `C:\private\canary`, `team\..\canary`, "canary:stream", "CON", "NUL.txt", "bad\nname", "."} {
		job.File = name
		if job.Execute().Err == nil {
			t.Fatal("unsafe filename accepted", name)
		}
	}
	job.Log, job.File = nil, "unconfigured.jsonl"
	if job.Execute().Err == nil {
		t.Fatal("missing root wrote into the working directory")
	}
}

func TestNotificationLogProtectedAndAliasedFiles(t *testing.T) {
	base := t.TempDir()
	state := filepath.Join(base, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{state, filepath.Join(state, "snapshots"), base} {
		if log, err := OpenNotificationLog(root, state); err == nil {
			log.Close()
			t.Fatal("protected state overlapped output root")
		}
	}
	root := filepath.Join(base, "notifications")
	log, err := OpenNotificationLog(root, state)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	canary := filepath.Join(state, "private")
	if err := os.WriteFile(canary, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(canary, filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	if (&CodeLogJob{Log: log, File: "alias"}).Execute().Err == nil {
		t.Fatal("hard-linked state accepted")
	}
	contents, err := os.ReadFile(canary)
	if err != nil || string(contents) != "unchanged" {
		t.Fatal("protected state changed", err)
	}
}

func TestNotificationLogProtectedDirectoryIdentity(t *testing.T) {
	base := t.TempDir()
	state := filepath.Join(base, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	stateHandle, err := os.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	stateInfo, err := stateHandle.Stat()
	stateHandle.Close()
	if err != nil {
		t.Fatal(err)
	}
	forbidden := filepath.Join(state, "uncreated", "segment.db")
	if acquired, _, err := acquireNotificationLogRoot(forbidden, stateInfo); err == nil {
		acquired.Close()
		t.Fatal("protected identity allowed descent")
	}
	if _, err := os.Stat(filepath.Dir(forbidden)); !os.IsNotExist(err) {
		t.Fatal("rejected root modified protected state", err)
	}
	root, ancestors, err := acquireNotificationLogRoot(filepath.Join(state, "events"))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if checkProtectedLogIdentities(ancestors, []string{state}) == nil {
		t.Fatal("identity check allowed logs inside state")
	}
	if checkProtectedLogIdentities(ancestors, []string{filepath.Join(state, "events", "new-state")}) == nil {
		t.Fatal("identity check allowed protected state inside logs")
	}
	if checkProtectedLogIdentities(ancestors, []string{filepath.Join(base, "separate-state")}) != nil {
		t.Fatal("identity check rejected a separate prospective directory")
	}
}
