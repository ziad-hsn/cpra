//go:build !windows

package secureconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"
)

func TestTokenFileUnixPermissionsAndSourceTypes(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "token")
	if err := os.WriteFile(path, []byte("disposable-token"), 0600); err != nil {
		t.Fatal(err)
	}
	opts := TokenFileOptions{Path: path, MaxBytes: 128}
	for _, mode := range []os.FileMode{0400, 0600, 0640, 0644, 0660, 0700} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		data, err := ReadTokenFile(t.Context(), opts)
		clear(data)
		if (mode == 0400 || mode == 0600) != (err == nil) {
			t.Fatalf("mode %o: %v", mode, err)
		}
		info, _ := os.Stat(path)
		if info.Mode().Perm() != mode {
			t.Fatal("reader changed caller permissions")
		}
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "second-link")
	if err := os.Link(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTokenFile(t.Context(), opts); !errors.Is(err, ErrTokenFilePolicy) {
		t.Fatal("hardlinked source accepted", err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(root, "fifo")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	opts.Path = fifo
	if _, err := ReadTokenFile(t.Context(), opts); !errors.Is(err, ErrTokenFileType) {
		t.Fatal("special source accepted", err)
	}
}

func TestTokenFileProjectedGenerationRotationAndEscape(t *testing.T) {
	root := t.TempDir()
	for _, generation := range []string{"..generation-one", "..generation-two"} {
		if err := os.Mkdir(filepath.Join(root, generation), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, generation, "token"), []byte(generation), 0400); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("..generation-one", filepath.Join(root, "..data")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..data/token", filepath.Join(root, "token")); err != nil {
		t.Fatal(err)
	}
	opts := TokenFileOptions{Path: filepath.Join(root, "token"), MaxBytes: 128}
	if _, err := ReadTokenFile(t.Context(), opts); !errors.Is(err, ErrTokenFilePolicy) {
		t.Fatal("projected source requires an explicit trusted mount", err)
	}
	opts.Root = root
	first, err := ReadTokenFile(t.Context(), opts)
	if err != nil || string(first) != "..generation-one" {
		t.Fatal("projected source", err)
	}
	clear(first)
	if err := os.Symlink("..generation-two", filepath.Join(root, "..next")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "..next"), filepath.Join(root, "..data")); err != nil {
		t.Fatal(err)
	}
	second, err := ReadTokenFile(t.Context(), opts)
	if err != nil || string(second) != "..generation-two" {
		t.Fatal("rotated source", err)
	}
	clear(second)
	if err := os.Remove(filepath.Join(root, "token")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../outside/token", filepath.Join(root, "token")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTokenFile(t.Context(), opts); !errors.Is(err, ErrTokenFilePolicy) {
		t.Fatal("escaped root", err)
	}
}

func TestTokenFileRootOwnedReaderGroup(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-owned projected Secret policy requires native administrator fixture")
	}
	root := t.TempDir()
	path := filepath.Join(root, "token")
	if err := os.WriteFile(path, []byte("group-reader-token"), 0440); err != nil {
		t.Fatal(err)
	}
	group := 1001
	if err := os.Chown(path, 0, group); err != nil {
		t.Fatal(err)
	}
	opts := TokenFileOptions{Path: path, Root: root, ReaderGroupID: &group, MaxBytes: 128}
	if data, err := ReadTokenFile(t.Context(), opts); err != nil {
		t.Fatal(err)
	} else {
		clear(data)
	}
	wrong, _ := TokenReaderGroup(strconv.Itoa(group + 1))
	opts.ReaderGroupID = wrong
	if _, err := ReadTokenFile(t.Context(), opts); !errors.Is(err, ErrTokenFilePolicy) {
		t.Fatal("wrong group", err)
	}
	opts.ReaderGroupID = &group
	if err := os.Chmod(path, 0660); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTokenFile(t.Context(), opts); !errors.Is(err, ErrTokenFilePolicy) {
		t.Fatal("group writer", err)
	}
}

func TestTokenFileRejectsWritableAncestors(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "untrusted")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "token")
	if err := os.WriteFile(path, []byte("token"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0777); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTokenFile(t.Context(), TokenFileOptions{Path: path, Root: root, MaxBytes: 128}); !errors.Is(err, ErrTokenFilePolicy) {
		t.Fatal("unsafe ancestor", err)
	}
}
