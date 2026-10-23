package secureconfig

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTokenFileBoundedNativeRead(t *testing.T) {
	key := testKeyFileOptions(t)
	if _, err := GenerateLocalKeyFile(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(key.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(want)
	opts := TokenFileOptions{Path: key.Path, MaxBytes: 32}
	got, err := ReadTokenFile(t.Context(), opts)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatal("opened token read", err)
	}
	clear(got)
	opts.MaxBytes = 31
	if data, err := ReadTokenFile(t.Context(), opts); !errors.Is(err, ErrTokenFileSize) || data != nil {
		t.Fatal("size limit", err)
	}
	opts.MaxBytes = 32
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ReadTokenFile(ctx, opts); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation", err)
	}
	opts.Path = filepath.Join(filepath.Dir(key.Path), "absent-private-token")
	if data, err := ReadTokenFile(t.Context(), opts); !errors.Is(err, os.ErrNotExist) || data != nil || strings.Contains(err.Error(), opts.Path) {
		t.Fatal("missing source leaked or changed category", err)
	}
}

func TestTokenReaderGroupValidation(t *testing.T) {
	if group, err := TokenReaderGroup(""); err != nil || group != nil {
		t.Fatal(err)
	}
	if group, err := TokenReaderGroup("1001"); err != nil || group == nil || *group != 1001 {
		t.Fatal(err)
	}
	for _, value := range []string{"-1", "4294967295", "4294967296", "untrusted\nname", "../readers"} {
		if group, err := TokenReaderGroup(value); !errors.Is(err, ErrTokenFilePolicy) || group != nil || strings.Contains(err.Error(), value) {
			t.Fatal("invalid group accepted or echoed")
		}
	}
}
