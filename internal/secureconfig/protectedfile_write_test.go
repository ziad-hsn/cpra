package secureconfig

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestWriteNewProtectedFileExactExclusivePublication(t *testing.T) {
	key := testKeyFileOptions(t)
	opts := ProtectedFileOptions{Path: key.Path, DataDirectory: key.DataDirectory, MaxBytes: 128}
	data := []byte("new-bearer-material-only-in-this-private-file\n")
	before := append([]byte(nil), data...)
	if err := WriteNewProtectedFile(t.Context(), opts, data); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, before) {
		t.Fatal("writer changed caller-owned input")
	}
	read, err := ReadProtectedFile(t.Context(), opts)
	if err != nil || !bytes.Equal(read, data) {
		t.Fatal("publication lost data or native protection", err)
	}
	if err := WriteNewProtectedFile(t.Context(), opts, []byte("replacement")); !errors.Is(err, os.ErrExist) || strings.Contains(err.Error(), string(data)) {
		t.Fatal("existing protected file was replaced or exposed", err)
	}
	read, err = ReadProtectedFile(t.Context(), opts)
	if err != nil || !bytes.Equal(read, data) {
		t.Fatal("failed replacement changed original output", err)
	}
	entries, err := os.ReadDir(filepath.Dir(opts.Path))
	if err != nil || len(entries) != 1 {
		t.Fatal("publication left temporary material", err)
	}
	if _, err := os.Stat(opts.DataDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("protected output operation initialized or changed the state directory")
	}
}

func TestWriteNewProtectedFilePreflightAndCanceledInput(t *testing.T) {
	key := testKeyFileOptions(t)
	opts := ProtectedFileOptions{Path: key.Path, DataDirectory: key.DataDirectory, MaxBytes: 128}
	for _, invalid := range []ProtectedFileOptions{
		{Path: "relative", DataDirectory: key.DataDirectory, MaxBytes: 128},
		{Path: key.Path, DataDirectory: "relative", MaxBytes: 128},
		{Path: filepath.Join(key.DataDirectory, "token"), DataDirectory: key.DataDirectory, MaxBytes: 128},
		{Path: key.Path, DataDirectory: key.DataDirectory, MaxBytes: 0},
		{Path: key.Path, DataDirectory: key.DataDirectory, MaxBytes: 1<<20 + 1},
	} {
		if err := WriteNewProtectedFile(t.Context(), invalid, []byte("secret")); !errors.Is(err, ErrKeyFile) || strings.Contains(err.Error(), "secret") {
			t.Fatal("invalid protected output accepted or revealed", err)
		}
	}
	for _, data := range [][]byte{nil, bytes.Repeat([]byte{'x'}, 129)} {
		if err := WriteNewProtectedFile(t.Context(), opts, data); !errors.Is(err, ErrKeyFile) {
			t.Fatal("invalid output size accepted", err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := WriteNewProtectedFile(ctx, opts, []byte("secret")); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled output request accepted", err)
	}
	if err := WriteNewProtectedFile(nil, opts, []byte("secret")); !errors.Is(err, ErrInvalidContext) {
		t.Fatal("nil context accepted", err)
	}
	if _, err := os.Stat(filepath.Dir(opts.Path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid input provisioned a directory")
	}
}

func TestWriteNewProtectedFileConcurrentIssuersDoNotOverwrite(t *testing.T) {
	key := testKeyFileOptions(t)
	opts := ProtectedFileOptions{Path: key.Path, DataDirectory: key.DataDirectory, MaxBytes: 128}
	// Provision only the private directory before racing independent writes.
	if err := prepareKeyDirectory(filepath.Dir(key.Path), key); err != nil {
		t.Fatal(err)
	}
	var won atomic.Int64
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value := bytes.Repeat([]byte{byte('a' + i)}, 43)
			err := WriteNewProtectedFile(t.Context(), opts, value)
			if err == nil {
				won.Add(1)
			} else if !errors.Is(err, os.ErrExist) {
				t.Error("raced publication failed for another reason", err)
			}
		}()
	}
	wg.Wait()
	read, err := ReadProtectedFile(t.Context(), opts)
	if won.Load() != 1 || err != nil || len(read) != 43 || !bytes.Equal(read, bytes.Repeat(read[:1], 43)) {
		t.Fatal("concurrent publication mixed or overwrote output", err)
	}
}

func TestWriteNewProtectedFileRejectsUnsafeDirectoryAndAlias(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Unix mode and symbolic-link scenario; Windows uses native DACL tests")
	}
	key := testKeyFileOptions(t)
	parent := filepath.Dir(key.Path)
	if err := os.Mkdir(parent, 0755); err != nil {
		t.Fatal(err)
	}
	opts := ProtectedFileOptions{Path: key.Path, DataDirectory: key.DataDirectory, MaxBytes: 128}
	if err := WriteNewProtectedFile(t.Context(), opts, []byte("secret")); !errors.Is(err, ErrKeyFile) {
		t.Fatal("broad existing directory permissions accepted", err)
	}
	if info, err := os.Stat(parent); err != nil || info.Mode().Perm() != 0755 {
		t.Fatal("provisioning silently repaired existing permissions")
	}
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(parent), "alias")
	if err := os.Symlink(parent, alias); err != nil {
		t.Fatal(err)
	}
	opts.Path = filepath.Join(alias, "token")
	if err := WriteNewProtectedFile(t.Context(), opts, []byte("secret")); !errors.Is(err, ErrKeyFile) {
		t.Fatal("new token published through a symbolic link", err)
	}
}
