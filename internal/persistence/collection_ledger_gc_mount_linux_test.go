package persistence

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCollectionGCNativeMountBoundary(t *testing.T) {
	if os.Getenv("CPRA_GC_PRIVATE_MOUNT_TEST") != "1" {
		// A separate user+mount namespace prevents these temporary fixtures from
		// changing host mount state. The child marks propagation private first.
		cmd := exec.Command(os.Args[0], "-test.run=^TestCollectionGCNativeMountBoundary$", "-test.v")
		cmd.Env = append(os.Environ(), "CPRA_GC_PRIVATE_MOUNT_TEST=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWUSER | unix.CLONE_NEWNS,
			UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Geteuid(), Size: 1}},
			GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getegid(), Size: 1}},
			GidMappingsEnableSetgroups: false}
		output, err := cmd.CombinedOutput()
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EINVAL) {
			t.Skip("host prohibits isolated user/mount namespaces; native bind-mount scenario unavailable")
		}
		if err != nil {
			t.Fatalf("isolated mount test: %v\n%s", err, output)
		}
		t.Logf("isolated native mount scenarios:\n%s", output)
		return
	}
	// An environment marker is not proof of isolation. Independently unshare on
	// this locked thread before any mount, even when invoked directly by a
	// privileged developer with the child marker already in the environment.
	runtime.LockOSThread()
	// Do not return a thread with a changed namespace to the scheduler. When
	// this test goroutine exits, the runtime discards its still-locked thread.
	if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
		t.Skip("fresh isolated mount namespace unavailable; no mount changes made")
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatal("isolate mount propagation", err)
	}
	for _, leaf := range []string{"", "ledger.db"} {
		// Keep every mount syscall on this same locked goroutine; t.Run would
		// move its body to a different goroutine/possibly different namespace.
		f := newCollectionGCFixture(t)
		r := f.open(t)
		source := filepath.Join(f.dir, f.protection.Current, leaf)
		target := filepath.Join(f.dir, f.old, leaf)
		if err := unix.Mount(source, target, "", unix.MS_BIND, ""); err != nil {
			t.Fatal("private fixture bind mount", err)
		}
		defer func() {
			if err := unix.Unmount(target, 0); err != nil {
				t.Error("private fixture unmount", err)
			}
		}()
		if _, err := r.Step(context.Background(), f.old); !errors.Is(err, errCollectionGCBlocked) {
			t.Fatal("bind-mounted generation admitted", err)
		}
		if _, err := os.Stat(filepath.Join(f.dir, collectionGCIntentName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("mount alias created a retirement intent", err)
		}
		f.assertProtected(t)
		t.Logf("native bind alias rejected: leaf %q", leaf)
	}
}
