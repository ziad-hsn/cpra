package secureconfig

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func testKeyFileOptions(t *testing.T) KeyFileOptions {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		t.Skip("native ACL verifier is not yet implemented on this OS")
	}
	base := keyFileTestDirectory(t)
	// Resolve the test harness's temporary directory, which may itself be an
	// alias; Generate deliberately refuses aliases supplied by its caller.
	base, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	return KeyFileOptions{Path: filepath.Join(base, "keys", "active.key"), DataDirectory: filepath.Join(base, "state")}
}

func TestKeyFileGenerateLoadAndRotation(t *testing.T) {
	opts := testKeyFileOptions(t)
	id, err := GenerateLocalKeyFile(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	first, err := LoadLocalKeyFile(context.Background(), opts)
	if err != nil || first.ID() != id {
		t.Fatalf("load identity: %v", err)
	}
	info, err := os.Stat(opts.Path)
	if err != nil || info.Size() != keySize {
		t.Fatalf("key file size: %v", err)
	}
	before, err := os.ReadFile(opts.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(before)
	if _, err := GenerateLocalKeyFile(context.Background(), opts); !errors.Is(err, os.ErrExist) {
		t.Fatalf("overwrite result: %v", err)
	}
	after, err := os.ReadFile(opts.Path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("exclusive generation changed existing key")
	}
	clear(after)
	sealed, err := testSealer(t, first).Seal(context.Background(), testBinding(), []byte("private configuration"))
	if err != nil {
		t.Fatal(err)
	}
	opts.Path = filepath.Join(filepath.Dir(opts.Path), "next.key")
	nextID, err := GenerateLocalKeyFile(context.Background(), opts)
	if err != nil || nextID == id {
		t.Fatalf("rotation generation: %v", err)
	}
	next, err := LoadLocalKeyFile(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testSealer(t, next).Open(context.Background(), testBinding(), sealed); !errors.Is(err, ErrMissingKey) {
		t.Fatalf("missing old key: %v", err)
	}
	got, err := testSealer(t, next, first).Open(context.Background(), testBinding(), sealed)
	if err != nil || string(got) != "private configuration" {
		t.Fatalf("retained key decryption: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(opts.Path))
	if err != nil || len(entries) != 2 {
		t.Fatalf("temporary key files remain: %v", err)
	}
}

func TestKeyFileInputFailuresDoNotProvision(t *testing.T) {
	opts := testKeyFileOptions(t)
	if _, err := LoadLocalKeyFile(context.Background(), opts); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing key: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(opts.Path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("load provisioned a directory")
	}
	for _, invalid := range []KeyFileOptions{
		{Path: "relative.key", DataDirectory: opts.DataDirectory},
		{Path: opts.Path, DataDirectory: "relative-state"},
		{Path: filepath.Join(opts.DataDirectory, "key"), DataDirectory: opts.DataDirectory},
		{Path: opts.DataDirectory, DataDirectory: opts.DataDirectory},
	} {
		if _, err := GenerateLocalKeyFile(context.Background(), invalid); !errors.Is(err, ErrKeyFile) {
			t.Fatalf("invalid options result: %v", err)
		}
	}
	if _, err := GenerateLocalKeyFile(nil, opts); !errors.Is(err, ErrInvalidContext) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := GenerateLocalKeyFile(ctx, opts); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := LoadLocalKeyFile(ctx, opts); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(opts.Path)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid request changed filesystem")
	}
}

func TestKeyFileMalformedContents(t *testing.T) {
	for _, size := range []int{0, 1, 31, 33, 4096} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			opts := testKeyFileOptions(t)
			if _, err := GenerateLocalKeyFile(context.Background(), opts); err != nil {
				t.Fatal(err)
			}
			secret := bytes.Repeat([]byte{'q'}, size)
			if err := os.WriteFile(opts.Path, secret, 0600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadLocalKeyFile(context.Background(), opts)
			if !errors.Is(err, ErrKeyFile) && !errors.Is(err, ErrInvalidKey) {
				t.Fatalf("size %d accepted: %v", size, err)
			}
			if strings.Contains(err.Error(), strings.Repeat("q", 16)) {
				t.Fatal("error includes key material")
			}
		})
	}
}

func TestKeyFileConcurrentExclusiveGeneration(t *testing.T) {
	opts := testKeyFileOptions(t)
	// Create/protect the directory once so this test isolates publication races.
	seed := opts
	seed.Path = filepath.Join(filepath.Dir(opts.Path), "seed.key")
	if _, err := GenerateLocalKeyFile(context.Background(), seed); err != nil {
		t.Fatal(err)
	}
	const contenders = 12
	results := make(chan error, contenders)
	var wg sync.WaitGroup
	for range contenders {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := GenerateLocalKeyFile(context.Background(), opts); results <- err }()
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, os.ErrExist) {
			t.Fatalf("unexpected concurrent failure: %v", err)
		}
	}
	if winners != 1 {
		t.Fatalf("got %d successful publishers", winners)
	}
	if _, err := LoadLocalKeyFile(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(opts.Path))
	if err != nil || len(entries) != 2 {
		t.Fatalf("temporary keys remain: %v", err)
	}
}
