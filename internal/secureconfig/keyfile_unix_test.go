//go:build linux

package secureconfig

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func keyFileTestDirectory(t *testing.T) string { t.Helper(); return t.TempDir() }

func TestKeyFileUnixPermissions(t *testing.T) {
	for _, mode := range []os.FileMode{0000, 0200, 0404, 0644, 0660, 0700, 0777, os.ModeSetuid | 0600} {
		t.Run(mode.String(), func(t *testing.T) {
			opts := testKeyFileOptions(t)
			if _, err := GenerateLocalKeyFile(context.Background(), opts); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(opts.Path, mode); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadLocalKeyFile(context.Background(), opts); err == nil {
				t.Fatalf("accepted key mode %v", mode)
			}
		})
	}
	for _, mode := range []os.FileMode{0400, 0600} {
		opts := testKeyFileOptions(t)
		if _, err := GenerateLocalKeyFile(context.Background(), opts); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(opts.Path, mode); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadLocalKeyFile(context.Background(), opts); err != nil {
			t.Fatalf("rejected valid mode: %v", err)
		}
	}
	opts := testKeyFileOptions(t)
	if _, err := GenerateLocalKeyFile(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(opts.Path), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLocalKeyFile(context.Background(), opts); !errors.Is(err, ErrKeyFile) {
		t.Fatalf("public directory accepted: %v", err)
	}
	if _, err := GenerateLocalKeyFile(context.Background(), KeyFileOptions{Path: filepath.Join(filepath.Dir(opts.Path), "other"), DataDirectory: opts.DataDirectory}); !errors.Is(err, ErrKeyFile) {
		t.Fatalf("generation changed unsafe directory: %v", err)
	}
}

func TestKeyFileAliasesAndStateSeparation(t *testing.T) {
	opts := testKeyFileOptions(t)
	id, err := GenerateLocalKeyFile(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Dir(filepath.Dir(opts.Path))
	alias := opts
	alias.Path = filepath.Join(base, "projected.key")
	if err := os.Symlink(opts.Path, alias.Path); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadLocalKeyFile(context.Background(), alias)
	if err != nil || loaded.ID() != id {
		t.Fatalf("external alias: %v", err)
	}
	if _, err := GenerateLocalKeyFile(context.Background(), alias); !errors.Is(err, ErrKeyFile) {
		t.Fatalf("generation followed alias: %v", err)
	}
	if err := os.Mkdir(opts.DataDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(opts.DataDirectory, "copy.key")
	if err := os.Rename(opts.Path, inside); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(alias.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inside, alias.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLocalKeyFile(context.Background(), alias); !errors.Is(err, ErrKeyFile) {
		t.Fatalf("state alias accepted: %v", err)
	}
	stateAlias := filepath.Join(base, "state-alias")
	if err := os.Symlink(opts.DataDirectory, stateAlias); err != nil {
		t.Fatal(err)
	}
	opts.Path = inside
	opts.DataDirectory = stateAlias
	if _, err := LoadLocalKeyFile(context.Background(), opts); !errors.Is(err, ErrKeyFile) {
		t.Fatalf("aliased state boundary bypassed: %v", err)
	}
}

func TestKeyFileRejectsIntermediateUntrustedAlias(t *testing.T) {
	opts := testKeyFileOptions(t)
	if _, err := GenerateLocalKeyFile(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	base := filepath.Dir(filepath.Dir(opts.Path))
	unsafe := filepath.Join(base, "writable")
	if err := os.Mkdir(unsafe, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafe, 0777); err != nil {
		t.Fatal(err)
	}
	intermediate := filepath.Join(unsafe, "indirect")
	if err := os.Symlink(opts.Path, intermediate); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "outer")
	if err := os.Symlink(intermediate, alias); err != nil {
		t.Fatal(err)
	}
	opts.Path = alias
	if _, err := LoadLocalKeyFile(context.Background(), opts); !errors.Is(err, ErrKeyFile) {
		t.Fatalf("intermediate alias bypassed ownership: %v", err)
	}
}

func TestKeyFileOpenedHandleMustMatchCurrentPath(t *testing.T) {
	opts := testKeyFileOptions(t)
	if _, err := GenerateLocalKeyFile(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(filepath.Dir(opts.Path))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, err := openKeyFile(root, filepath.Base(opts.Path))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := os.Rename(opts.Path, opts.Path+".old"); err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateLocalKeyFile(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if err := verifyKeyAccess(file, root, opts, opts.Path); !errors.Is(err, ErrKeyFileChanged) {
		t.Fatalf("replacement escaped handle check: %v", err)
	}
	if err := os.Rename(filepath.Dir(opts.Path), filepath.Dir(opts.Path)+".old"); err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateLocalKeyFile(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if err := verifyKeyDirectory(root, filepath.Dir(opts.Path)); !errors.Is(err, ErrKeyFileChanged) {
		t.Fatalf("directory replacement escaped handle check: %v", err)
	}
}

func TestKeyFileRejectsHardlinksAndNonregularFiles(t *testing.T) {
	opts := testKeyFileOptions(t)
	if _, err := GenerateLocalKeyFile(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(opts.DataDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(opts.Path, filepath.Join(opts.DataDirectory, "key-alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLocalKeyFile(context.Background(), opts); !errors.Is(err, ErrKeyFile) {
		t.Fatalf("hard-linked key accepted: %v", err)
	}
	if err := os.Remove(opts.Path); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(opts.Path, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := LoadLocalKeyFile(context.Background(), opts); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrKeyFile) {
			t.Fatalf("FIFO: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("loading FIFO blocked before regular-file check")
	}
}

func TestKeyFileReaderPolicyValidation(t *testing.T) {
	opts := testKeyFileOptions(t)
	opts.ReaderSID = "S-1-5-18"
	if _, err := GenerateLocalKeyFile(context.Background(), opts); !errors.Is(err, ErrKeyFile) {
		t.Fatal(err)
	}
	opts.ReaderSID = ""
	group := -1
	opts.ReaderGroupID = &group
	if _, err := GenerateLocalKeyFile(context.Background(), opts); !errors.Is(err, ErrKeyFile) {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		t.Skip("non-root provisioning rejection requires a non-root process")
	}
	group = os.Getegid()
	if _, err := GenerateLocalKeyFile(context.Background(), opts); !errors.Is(err, ErrKeyFile) {
		t.Fatalf("non-root shared key generated: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(opts.Path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected request left a directory")
	}
}

func TestKeyFileRejectsExtendedReaderACL(t *testing.T) {
	opts := testKeyFileOptions(t)
	if _, err := GenerateLocalKeyFile(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	// Linux POSIX ACL: owner rw, named uid 65534 read, group none, mask read,
	// other none. Mode 0640 alone cannot identify the extra named reader.
	acl := []byte{2, 0, 0, 0, 1, 0, 6, 0, 255, 255, 255, 255, 2, 0, 4, 0, 254, 255, 0, 0, 4, 0, 0, 0, 255, 255, 255, 255, 16, 0, 4, 0, 255, 255, 255, 255, 32, 0, 0, 0, 255, 255, 255, 255}
	if err := unix.Setxattr(opts.Path, "system.posix_acl_access", acl, 0); err != nil {
		if errors.Is(err, unix.ENOTSUP) {
			t.Skip("filesystem does not support POSIX ACLs")
		}
		t.Fatal(err)
	}
	file, err := os.Open(opts.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := checkKeyAccessACL(file, ""); !errors.Is(err, ErrKeyFile) {
		t.Fatalf("named reader ACL accepted: %v", err)
	}
}

func TestKeyFileCrashAfterExclusivePublication(t *testing.T) {
	opts := testKeyFileOptions(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestKeyFilePublicationCrashHelper$")
	cmd.Env = append(os.Environ(), "CPRA_KEY_CRASH_PATH="+opts.Path)
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	line := bufio.NewScanner(output)
	if !line.Scan() || line.Text() != "published" {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		waited = true
		t.Fatalf("child did not publish key: %v, %s", line.Err(), stderr.String())
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("crash helper exited normally")
	}
	waited = true
	loaded, err := LoadLocalKeyFile(context.Background(), opts)
	if err != nil {
		t.Fatalf("published key is unloadable after SIGKILL: %v", err)
	}
	want, err := NewLocalWrapper(bytes.Repeat([]byte{73}, keySize))
	if err != nil || loaded.ID() != want.ID() {
		t.Fatal("restart loaded a different key")
	}
	entries, err := os.ReadDir(filepath.Dir(opts.Path))
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(opts.Path) {
		t.Fatalf("publication left a temporary name: %v", err)
	}
}

func TestKeyFilePublicationCrashHelper(t *testing.T) {
	path := os.Getenv("CPRA_KEY_CRASH_PATH")
	if path == "" {
		return
	}
	if err := os.Mkdir(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	file, err := root.OpenFile("temporary", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(bytes.Repeat([]byte{73}, keySize)); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	dir, err := root.Open(".")
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if err := publishKeyName(dir, "temporary", filepath.Base(path)); err != nil {
		t.Fatal(err)
	}
	// The parent kills this process before directory Sync or any cleanup. This
	// checks process-crash recovery, not persistence after physical power loss.
	fmt.Println("published")
	time.Sleep(time.Hour)
}
