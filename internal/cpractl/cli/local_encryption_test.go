package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

func runLocalEncryption(ctx context.Context, args ...string) (string, error) {
	root := NewRootCommand()
	var output bytes.Buffer
	root.SetOut(&output)
	root.SetErr(&output)
	root.SetArgs(append([]string{"local", "encryption"}, args...))
	err := root.ExecuteContext(ctx)
	return output.String(), err
}

func localEncryptionPaths(t *testing.T) secureconfig.KeyFileOptions {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("Unix CLI filesystem checks; native Windows key protection is qualified separately")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return secureconfig.KeyFileOptions{Path: filepath.Join(base, "keys", "wrapping.key"), DataDirectory: filepath.Join(base, "state")}
}

func localEncryptionIdentity(t *testing.T, output string) string {
	t.Helper()
	var report map[string]string
	if err := json.Unmarshal([]byte(output), &report); err != nil || len(report) != 1 || report["key_id"] == "" {
		t.Fatal("command did not return only the key identity")
	}
	return report["key_id"]
}

func TestLocalEncryptionCreateInspectAndNoRemoteAccess(t *testing.T) {
	key := localEncryptionPaths(t)
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	t.Cleanup(server.Close)
	t.Setenv("CPRA_SERVER", server.URL)
	t.Setenv("CPRA_AUTH_TOKEN_FILE", filepath.Join(filepath.Dir(key.DataDirectory), "absent-api-token"))
	args := []string{"--key-file", key.Path, "--data-dir", key.DataDirectory}
	created, err := runLocalEncryption(t.Context(), append([]string{"init"}, args...)...)
	if err != nil {
		t.Fatal(err)
	}
	id := localEncryptionIdentity(t, created)
	wrapper, err := secureconfig.LoadLocalKeyFile(t.Context(), key)
	if err != nil || wrapper.ID() != id {
		t.Fatal("created key cannot be loaded with the declared policy", err)
	}
	before, err := os.ReadFile(key.Path)
	if err != nil || len(before) != 32 {
		t.Fatal("created key is not exactly 32 bytes", err)
	}
	defer clear(before)
	inspected, err := runLocalEncryption(t.Context(), append([]string{"inspect"}, args...)...)
	if err != nil || localEncryptionIdentity(t, inspected) != id {
		t.Fatal("inspection changed key identity", err)
	}
	repeated, repeatErr := runLocalEncryption(t.Context(), append([]string{"init"}, args...)...)
	if !errors.Is(repeatErr, os.ErrExist) || repeated != "" {
		t.Fatal("initialization did not refuse an existing key", repeatErr)
	}
	after, err := os.ReadFile(key.Path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("inspection or repeated initialization changed key bytes", err)
	}
	clear(after)
	combined := created + inspected + repeated + repeatErr.Error()
	for _, secret := range []string{string(before), hex.EncodeToString(before), base64.StdEncoding.EncodeToString(before), base64.RawStdEncoding.EncodeToString(before)} {
		if strings.Contains(combined, secret) {
			t.Fatal("command disclosed key material")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("local key administration contacted the API")
	}
	if _, err := os.Stat(key.DataDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("local key administration created state", err)
	}
	entries, err := os.ReadDir(filepath.Dir(key.Path))
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(key.Path) {
		t.Fatal("key administration left unexpected files", err)
	}
	for path, want := range map[string]os.FileMode{key.Path: 0600, filepath.Dir(key.Path): 0700} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != want {
			t.Fatal("owner-only key protection was not applied", err)
		}
	}
}

func TestLocalEncryptionRejectsInvalidInputBeforeProvisioning(t *testing.T) {
	base := t.TempDir()
	key, state := filepath.Join(base, "keys", "wrapping.key"), filepath.Join(base, "state")
	for _, action := range []string{"init", "inspect"} {
		for _, tc := range []struct {
			name string
			args []string
		}{
			{"missing-both", nil},
			{"missing-state", []string{"--key-file", key}},
			{"missing-key", []string{"--data-dir", state}},
			{"relative-key", []string{"--key-file", "relative.key", "--data-dir", state}},
			{"relative-state", []string{"--key-file", key, "--data-dir", "state"}},
			{"state-is-key", []string{"--key-file", state, "--data-dir", state}},
			{"key-in-state", []string{"--key-file", filepath.Join(state, "key"), "--data-dir", state}},
			{"extra-argument", []string{"unexpected", "--key-file", key, "--data-dir", state}},
			{"force", []string{"--force", "--key-file", key, "--data-dir", state}},
			{"negative-group", []string{"--reader-group-id", "-1", "--key-file", key, "--data-dir", state}},
			{"conflicting-readers", []string{"--reader-group-id", "0", "--reader-sid", "S-1-5-19", "--key-file", key, "--data-dir", state}},
			{"empty-sid", []string{"--reader-sid", "", "--key-file", key, "--data-dir", state}},
		} {
			t.Run(action+"/"+tc.name, func(t *testing.T) {
				output, err := runLocalEncryption(t.Context(), append([]string{action}, tc.args...)...)
				if err == nil || output != "" {
					t.Fatal("invalid key request was not rejected before output", err)
				}
				entries, err := os.ReadDir(base)
				if err != nil || len(entries) != 0 {
					t.Fatal("invalid key request changed the filesystem", err)
				}
			})
		}
	}
}

func TestLocalEncryptionCancellationAndMissingInspection(t *testing.T) {
	key := localEncryptionPaths(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, action := range []string{"init", "inspect"} {
		output, err := runLocalEncryption(ctx, action, "--key-file", key.Path, "--data-dir", key.DataDirectory)
		if !errors.Is(err, context.Canceled) || output != "" {
			t.Fatal("cancelled command did not preserve cancellation", err)
		}
	}
	output, err := runLocalEncryption(t.Context(), "inspect", "--key-file", key.Path, "--data-dir", key.DataDirectory)
	if !errors.Is(err, os.ErrNotExist) || output != "" {
		t.Fatal("missing key inspection did not preserve the error", err)
	}
	entries, err := os.ReadDir(filepath.Dir(key.DataDirectory))
	if err != nil || len(entries) != 0 {
		t.Fatal("cancelled command or inspection provisioned files", err)
	}
}

func TestLocalEncryptionDoesNotRepairUnsafePermissions(t *testing.T) {
	key := localEncryptionPaths(t)
	if err := os.Mkdir(filepath.Dir(key.Path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(key.Path), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := runLocalEncryption(t.Context(), "init", "--key-file", key.Path, "--data-dir", key.DataDirectory); !errors.Is(err, secureconfig.ErrKeyFile) {
		t.Fatal("unsafe directory accepted", err)
	}
	info, err := os.Stat(filepath.Dir(key.Path))
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatal("command changed existing directory permissions", err)
	}
	if err := os.Chmod(filepath.Dir(key.Path), 0700); err != nil {
		t.Fatal(err)
	}
	canary := []byte("secret-key-canary-do-not-print!!")
	if err := os.WriteFile(key.Path, canary, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(key.Path, 0644); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"init", "inspect"} {
		output, err := runLocalEncryption(t.Context(), action, "--key-file", key.Path, "--data-dir", key.DataDirectory)
		if err == nil || output != "" || strings.Contains(err.Error(), string(canary)) {
			t.Fatal("unsafe key was accepted or disclosed")
		}
	}
	info, err = os.Stat(key.Path)
	if err != nil || info.Mode().Perm() != 0644 {
		t.Fatal("command repaired existing key permissions", err)
	}
	content, err := os.ReadFile(key.Path)
	if err != nil || !bytes.Equal(content, canary) {
		t.Fatal("command replaced unsafe key material", err)
	}
}

func TestLocalEncryptionExplicitGroupIsNotOwnerMode(t *testing.T) {
	key := localEncryptionPaths(t)
	group := os.Getgid()
	args := []string{"--key-file", key.Path, "--data-dir", key.DataDirectory, "--reader-group-id", strconv.Itoa(group)}
	output, err := runLocalEncryption(t.Context(), append([]string{"init"}, args...)...)
	if os.Geteuid() != 0 {
		if !errors.Is(err, secureconfig.ErrKeyFile) || output != "" {
			t.Fatal("unprivileged service key request silently became owner-only", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	id := localEncryptionIdentity(t, output)
	inspected, err := runLocalEncryption(t.Context(), append([]string{"inspect"}, args...)...)
	if err != nil || localEncryptionIdentity(t, inspected) != id {
		t.Fatal("service reader policy was not retained", err)
	}
	for path, want := range map[string]os.FileMode{key.Path: 0640, filepath.Dir(key.Path): 0750} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != want {
			t.Fatal("service reader permissions were not applied", err)
		}
	}
}

type failedKeyOutput struct{}

func (failedKeyOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestLocalEncryptionOutputFailurePreservesCreatedKey(t *testing.T) {
	key := localEncryptionPaths(t)
	args := []string{"--key-file", key.Path, "--data-dir", key.DataDirectory}
	root := NewRootCommand()
	root.SetOut(failedKeyOutput{})
	root.SetArgs(append([]string{"local", "encryption", "init"}, args...))
	err := root.ExecuteContext(t.Context())
	if !errors.Is(err, io.ErrClosedPipe) || !strings.Contains(err.Error(), "key created") || !strings.Contains(err.Error(), "local encryption inspect") {
		t.Fatal("output failure did not explain the committed file", err)
	}
	before, err := os.ReadFile(key.Path)
	if err != nil || len(before) != 32 {
		t.Fatal("output failure removed the created key", err)
	}
	defer clear(before)
	output, err := runLocalEncryption(t.Context(), append([]string{"inspect"}, args...)...)
	if err != nil {
		t.Fatal(err)
	}
	localEncryptionIdentity(t, output)
	if _, err := runLocalEncryption(t.Context(), append([]string{"init"}, args...)...); !errors.Is(err, os.ErrExist) {
		t.Fatal("retry replaced key after output failure", err)
	}
	after, err := os.ReadFile(key.Path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("reconciliation changed the original key", err)
	}
	clear(after)
}
