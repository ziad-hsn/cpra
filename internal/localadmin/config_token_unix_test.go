//go:build linux || darwin

package localadmin

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

func TestUnixInitRefusesUnsafeSuppliedTokenWithoutRepair(t *testing.T) {
	for _, mode := range []string{"other-read", "group-read", "group-write", "other-write", "hard-link", "directory-write", "state-overlap", "log-overlap", "relative-config-overlap", "relative-state-overlap"} {
		t.Run(mode, func(t *testing.T) {
			l := testLayout(t)
			if err := os.MkdirAll(l.ConfigDir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(l.ConfigDir, "auth.token")
			data := []byte(strings.Repeat("supplied-token-marker", 3))
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			var err error
			switch mode {
			case "other-read":
				err = os.Chmod(path, 0604)
			case "group-read":
				err = os.Chmod(path, 0640)
			case "group-write":
				err = os.Chmod(path, 0620)
			case "other-write":
				err = os.Chmod(path, 0602)
			case "hard-link":
				err = os.Link(path, filepath.Join(filepath.Dir(l.ConfigDir), "supplied-token-link"))
			case "directory-write":
				err = os.Chmod(l.ConfigDir, 0777)
			case "state-overlap":
				l.StateDir = l.ConfigDir
			case "log-overlap":
				l.LogDir = l.ConfigDir
			case "relative-config-overlap", "relative-state-overlap":
				workingDirectory, workingErr := os.Getwd()
				if workingErr != nil {
					t.Fatal(workingErr)
				}
				relative, relativeErr := filepath.Rel(workingDirectory, l.ConfigDir)
				if relativeErr != nil {
					t.Fatal(relativeErr)
				}
				if mode == "relative-config-overlap" {
					l.StateDir, l.ConfigDir = l.ConfigDir, relative
				} else {
					l.StateDir = relative
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			directory, err := os.Lstat(l.ConfigDir)
			if err != nil {
				t.Fatal(err)
			}
			err = Init(l, "", 0)
			if err == nil || !strings.Contains(err.Error(), "existing API token") || strings.Contains(err.Error(), "supplied-token-marker") {
				t.Fatal("unsafe token was accepted or its contents entered the diagnostic")
			}
			if strings.HasSuffix(mode, "overlap") && !strings.Contains(err.Error(), "overlaps writable state or logs") {
				t.Fatal("overlapping layout was not rejected before token preparation", err)
			}
			assertUnixTokenUnchanged(t, path, before, data)
			assertUnixIdentityUnchanged(t, l.ConfigDir, directory)
		})
	}
}

func TestUnixInitPreservesProtectedSuppliedToken(t *testing.T) {
	l := testLayout(t)
	if err := Init(l, "", 0); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(l.ConfigDir, "auth.token")
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Init(l, "", 0); err != nil {
		t.Fatal(err)
	}
	assertUnixTokenUnchanged(t, path, before, data)
	loaded, err := secureconfig.ReadTokenFile(t.Context(), secureconfig.TokenFileOptions{Path: path, Root: l.ConfigDir, MaxBytes: 4096})
	defer clear(loaded)
	if err != nil || !bytes.Equal(loaded, data) {
		t.Fatal("preserved token does not satisfy the runtime reader policy", err)
	}
}

func TestUnixSystemTokenCreationAndSuppliedOwnership(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires native administrator ownership; no system-token deployment claim on an unprivileged runner")
	}
	account := "daemon"
	if runtime.GOOS == "darwin" {
		account = "_daemon"
	}
	var nativeUID string
	if runtime.GOOS == "darwin" {
		// Match the native account database used by CGO-disabled production
		// binaries; os/user.Lookup cannot resolve these macOS identities.
		output, err := exec.CommandContext(t.Context(), "/usr/bin/id", "-u", account).Output()
		if err != nil {
			t.Skip("native test service identity is unavailable")
		}
		nativeUID = strings.TrimSpace(string(output))
	} else {
		identity, err := user.Lookup(account)
		if err != nil {
			t.Skip("native test service identity is unavailable")
		}
		nativeUID = identity.Uid
	}
	uid, err := strconv.Atoi(nativeUID)
	if err != nil || uid == 0 {
		t.Fatal("native service fixture must be non-root", err)
	}
	for _, supplied := range []bool{false, true} {
		t.Run(strconv.FormatBool(supplied), func(t *testing.T) {
			l := testLayout(t)
			l.Scope = "system"
			group, _, err := managementServiceReader(l, account)
			if err != nil || group == nil {
				t.Fatal("service reader identity", err)
			}
			for _, path := range []string{l.ConfigDir, l.StateDir, l.LogDir} {
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := nativeSecure(l, account); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(l.ConfigDir, "auth.token")
			data := []byte(strings.Repeat("system-token-fixture", 3))
			if err := writeInitialConfig(l, account, "auth.token", data); err != nil {
				t.Fatal(err)
			}
			loaded, err := secureconfig.ReadTokenFile(t.Context(), secureconfig.TokenFileOptions{Path: path, Root: l.ConfigDir, ReaderGroupID: group, MaxBytes: 4096})
			clear(loaded)
			if err != nil {
				t.Fatal("new token lacked final reader protection", err)
			}
			if err := writeInitialConfig(l, account, "auth.token", []byte("replacement")); !errors.Is(err, os.ErrExist) {
				t.Fatal("new-file publication replaced an existing token", err)
			}
			if supplied {
				if err := os.Chown(path, uid, *group); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Chmod(path, 0440); err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			err = nativeSecure(l, account)
			if supplied && err == nil || !supplied && err != nil {
				t.Fatal("incorrect supplied-token policy", err)
			}
			assertUnixTokenUnchanged(t, path, before, data)
		})
	}
}

func assertUnixTokenUnchanged(t *testing.T, path string, before os.FileInfo, data []byte) {
	t.Helper()
	assertUnixIdentityUnchanged(t, path, before)
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, data) {
		t.Fatal("supplied token contents changed", err)
	}
}

func assertUnixIdentityUnchanged(t *testing.T, path string, before os.FileInfo) {
	t.Helper()
	after, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	oldNative, oldOK := before.Sys().(*syscall.Stat_t)
	newNative, newOK := after.Sys().(*syscall.Stat_t)
	if !oldOK || !newOK || !os.SameFile(before, after) || before.Mode() != after.Mode() || oldNative.Uid != newNative.Uid || oldNative.Gid != newNative.Gid {
		t.Fatal("supplied credential or parent protection changed")
	}
}
