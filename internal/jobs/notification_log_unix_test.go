//go:build !windows

package jobs

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestNotificationLogRejectsLinksAndFIFO(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "logs")
	log, err := OpenNotificationLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	if err := os.Symlink(base, filepath.Join(dir, "escape")); err != nil {
		t.Fatal(err)
	}
	if (&CodeLogJob{Log: log, File: "escape/canary"}).Execute().Err == nil {
		t.Fatal("symlink escaped root")
	}
	if _, err := os.Stat(filepath.Join(base, "canary")); !os.IsNotExist(err) {
		t.Fatal("symlink attack created outside file", err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- (&CodeLogJob{Log: log, File: "pipe"}).Execute().Err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO blocked a worker")
	}
}

func TestNotificationLogRejectsWritableAncestorAndPublicRoot(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0777); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(parent, "notifications")
	if log, err := OpenNotificationLog(directory); err == nil {
		log.Close()
		t.Fatal("untrusted directory replacement rights accepted")
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("unsafe ancestor created output")
	}
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0755); err != nil {
		t.Fatal(err)
	}
	if log, err := OpenNotificationLog(directory); err == nil {
		log.Close()
		t.Fatal("public root accepted")
	}
}

func TestNotificationLogRejectsRelativeAliases(t *testing.T) {
	base := t.TempDir()
	state := filepath.Join(base, "state")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink("state", alias); err != nil {
		t.Fatal(err)
	}
	if log, err := OpenNotificationLog(filepath.Join(alias, "events"), state); err == nil {
		log.Close()
		t.Fatal("relative alias into protected state accepted")
	}
	if _, err := os.Stat(filepath.Join(state, "events")); !os.IsNotExist(err) {
		t.Fatal("alias created a directory in protected state")
	}
	root := filepath.Join(base, "notifications")
	log, err := OpenNotificationLog(root, state)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	if err := os.Mkdir(filepath.Join(root, "team"), 0700); err != nil {
		t.Fatal(err)
	}
	canary := filepath.Join(root, "team", "private")
	if err := os.WriteFile(canary, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("team", filepath.Join(root, "directory-alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("team/private", filepath.Join(root, "file-alias")); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"directory-alias/private", "file-alias"} {
		if (&CodeLogJob{Log: log, File: file}).Execute().Err == nil {
			t.Fatal("relative alias within root accepted", file)
		}
	}
	got, err := os.ReadFile(canary)
	if err != nil || string(got) != "unchanged" {
		t.Fatal("alias modified private file", err)
	}
}

func TestNotificationLogResolvesProtectedAncestors(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "notifications")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink("notifications", alias); err != nil {
		t.Fatal(err)
	}
	protected := filepath.Join(alias, "future", "state")
	resolved, err := resolveLogProtectedPath(protected)
	if err != nil || resolved != filepath.Join(root, "future", "state") {
		t.Fatal("protected ancestor did not resolve", err)
	}
	if log, err := OpenNotificationLog(root, protected); err == nil {
		log.Close()
		t.Fatal("protected prospective directory was allowed inside log root")
	}
	if err := os.Symlink("notifications/future-state", filepath.Join(base, "dangling")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(base, "dangling"), filepath.Join(base, "dangling", "child")} {
		if log, err := OpenNotificationLog(root, path); err == nil {
			log.Close()
			t.Fatal("dangling protected alias accepted")
		}
	}
}
