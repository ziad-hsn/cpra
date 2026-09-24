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
	"testing"
)

func TestProtectedFileBoundsOwnershipAndExactKeySeparation(t *testing.T) {
	keys := testKeyFileOptions(t)
	if _, err := GenerateLocalKeyFile(context.Background(), keys); err != nil {
		t.Fatal(err)
	}
	source := []byte("test-bootstrap-token-value")
	if err := os.WriteFile(keys.Path, source, 0600); err != nil {
		t.Fatal(err)
	}
	opts := ProtectedFileOptions{Path: keys.Path, DataDirectory: keys.DataDirectory, MaxBytes: int64(len(source))}
	data, err := ReadProtectedFile(context.Background(), opts)
	if err != nil || !bytes.Equal(data, source) {
		t.Fatal("bounded read", err)
	}
	data[0] = 'X'
	reread, err := ReadProtectedFile(context.Background(), opts)
	if err != nil || !bytes.Equal(reread, source) {
		t.Fatal("reader shares mutable bytes", err)
	}
	clear(data)
	clear(reread)
	if _, err := LoadLocalKeyFile(context.Background(), keys); err == nil {
		t.Fatal("protected-source support weakened exact key length")
	}
	for _, limit := range []int64{0, -1, int64(len(source) - 1), (1 << 20) + 1} {
		opts.MaxBytes = limit
		result, err := ReadProtectedFile(context.Background(), opts)
		if !errors.Is(err, ErrKeyFile) || result != nil {
			t.Fatal("invalid bound accepted", err)
		}
		if strings.Contains(err.Error(), string(source)) || strings.Contains(err.Error(), keys.Path) {
			t.Fatal("source leaked")
		}
	}
	opts.MaxBytes = 1024
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadProtectedFile(canceled, opts); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := ReadProtectedFile(nil, opts); !errors.Is(err, ErrInvalidContext) {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 10 {
				data, err := ReadProtectedFile(context.Background(), opts)
				if err != nil || !bytes.Equal(data, source) {
					t.Error("concurrent read", err)
				}
				clear(data)
			}
		}()
	}
	wg.Wait()
}

func TestProtectedFilePermissionsAndExternalAliases(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Unix permissions and symlink scenario")
	}
	keys := testKeyFileOptions(t)
	if _, err := GenerateLocalKeyFile(context.Background(), keys); err != nil {
		t.Fatal(err)
	}
	opts := ProtectedFileOptions{Path: keys.Path, DataDirectory: keys.DataDirectory, MaxBytes: 100}
	if err := os.Chmod(keys.Path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadProtectedFile(context.Background(), opts); !errors.Is(err, ErrKeyFile) {
		t.Fatal("world read accepted", err)
	}
	if err := os.Chmod(keys.Path, 0600); err != nil {
		t.Fatal(err)
	}
	alias := opts
	alias.Path = keys.Path + ".alias"
	if err := os.Symlink(keys.Path, alias.Path); err != nil {
		t.Fatal(err)
	}
	data, err := ReadProtectedFile(context.Background(), alias)
	if err != nil || len(data) != 32 {
		t.Fatal("protected external alias", err)
	}
	clear(data)
	if err := os.Mkdir(keys.DataDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(keys.DataDirectory, "source")
	if err := os.WriteFile(inside, bytes.Repeat([]byte{'a'}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(alias.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inside, alias.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadProtectedFile(context.Background(), alias); !errors.Is(err, ErrKeyFile) {
		t.Fatal("alias into state accepted", err)
	}
	maxGID := int(uint64(1<<32) - 1)
	opts.ReaderGroupID = &maxGID
	if _, err := ReadProtectedFile(context.Background(), opts); !errors.Is(err, ErrKeyFile) {
		t.Fatal("chown no-change GID accepted", err)
	}
}
