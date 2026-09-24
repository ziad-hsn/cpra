package encryptionsetup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
)

func fixture(t *testing.T) (Options, secureconfig.KeyFileOptions) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("factory filesystem execution fixture qualified on Linux; native key boundary tested separately")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key := secureconfig.KeyFileOptions{Path: filepath.Join(base, "keys", "active.key"), DataDirectory: filepath.Join(base, "state")}
	opts := Options{StorageMode: "raft", DataDirectory: key.DataDirectory, Encryption: &runtimeconfig.ManagementEncryption{Local: &runtimeconfig.ManagementLocalKeys{ActiveKeyFile: key.Path}}}
	return opts, key
}
func binding() secureconfig.Binding {
	return secureconfig.Binding{StoreID: "store-1", Kind: "credential", ID: "database", UID: "uid-1", Revision: "1", Purpose: "configuration"}
}

func TestMemoryEphemeralExplicitAndIsolated(t *testing.T) {
	ctx := context.Background()
	opts := Options{StorageMode: "memory"}
	first, err := Open(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	encrypted, err := first.Sealer().Seal(ctx, binding(), []byte("fixture-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Sealer().Open(ctx, binding(), encrypted); !errors.Is(err, secureconfig.ErrMissingKey) {
		t.Fatal("ephemeral keys reused", err)
	}
	plain, err := first.Sealer().Open(ctx, binding(), encrypted)
	if err != nil || string(plain) != "fixture-secret" {
		t.Fatal(err)
	}
	clear(plain)
	if _, err := Open(ctx, Options{StorageMode: "raft"}); !errors.Is(err, ErrEncryptionRequired) {
		t.Fatal("durable silently fell back", err)
	}
	if _, err := Open(ctx, Options{StorageMode: ""}); !errors.Is(err, ErrConfiguration) {
		t.Fatal("implicit memory selected", err)
	}
	var count atomic.Int32
	handle := &Handle{closeFuncs: []func(){func() { count.Add(1) }}}
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := handle.Close(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if count.Load() != 1 {
		t.Fatal("owned transport closed repeatedly")
	}
	var absent *Handle
	if absent.Sealer() != nil || absent.Close() != nil {
		t.Fatal("nil handle")
	}
}

func TestLocalFactoryRestartAndPreviousKeys(t *testing.T) {
	opts, key := fixture(t)
	ctx := context.Background()
	if _, err := secureconfig.GenerateLocalKeyFile(ctx, key); err != nil {
		t.Fatal(err)
	}
	first, err := Open(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := first.Sealer().Seal(ctx, binding(), []byte("write-only fixture value"))
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	plain, err := restarted.Sealer().Open(ctx, binding(), encrypted)
	if err != nil || string(plain) != "write-only fixture value" {
		t.Fatal("restart", err)
	}
	clear(plain)
	next := key
	next.Path = filepath.Join(filepath.Dir(key.Path), "next.key")
	if _, err := secureconfig.GenerateLocalKeyFile(ctx, next); err != nil {
		t.Fatal(err)
	}
	opts.Encryption.Local.ActiveKeyFile = next.Path
	newOnly, err := Open(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer newOnly.Close()
	if _, err := newOnly.Sealer().Open(ctx, binding(), encrypted); !errors.Is(err, secureconfig.ErrMissingKey) {
		t.Fatal("missing epoch accepted", err)
	}
	opts.Encryption.Local.PreviousKeyFiles = []string{key.Path}
	rotated, err := Open(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer rotated.Close()
	plain, err = rotated.Sealer().Open(ctx, binding(), encrypted)
	if err != nil {
		t.Fatal("old epoch unavailable", err)
	}
	clear(plain)
	fresh, err := rotated.Sealer().Seal(ctx, binding(), []byte("new configuration"))
	if err != nil || fresh.KeyID == encrypted.KeyID {
		t.Fatal("active epoch not selected", err)
	}
	if _, err := os.Stat(opts.DataDirectory); !os.IsNotExist(err) {
		t.Fatal("factory opened or created durable storage", err)
	}
	opts.StorageMode = "memory"
	configured, err := Open(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer configured.Close()
	plain, err = configured.Sealer().Open(ctx, binding(), encrypted)
	if err != nil {
		t.Fatal("explicit memory key ignored", err)
	}
	clear(plain)
}

func TestFactoryMissingCorruptSourcesNeverProvisionOrFallback(t *testing.T) {
	opts, key := fixture(t)
	ctx := context.Background()
	for _, mode := range []string{"raft", "memory"} {
		opts.StorageMode = mode
		_, err := Open(ctx, opts)
		if !errors.Is(err, ErrBackend) || !errors.Is(err, os.ErrNotExist) {
			t.Fatal("missing source", err)
		}
		if strings.Contains(err.Error(), key.Path) {
			t.Fatal("source path leaked")
		}
	}
	if _, err := os.Stat(filepath.Dir(key.Path)); !os.IsNotExist(err) {
		t.Fatal("provisioned absent source", err)
	}
	if _, err := secureconfig.GenerateLocalKeyFile(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key.Path, []byte("private-malformed-key-sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := Open(ctx, opts)
	if !errors.Is(err, ErrBackend) || strings.Contains(err.Error(), "private-malformed-key-sentinel") {
		t.Fatal("corrupt source redaction", err)
	}
	if strings.Contains(fmt.Sprintf("%#v %v", opts, opts), key.Path) {
		t.Fatal("Options printed source")
	}
	if _, err := Open(nil, opts); !errors.Is(err, secureconfig.ErrInvalidContext) {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Open(canceled, opts); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestFactoryRejectsDescriptorsBeforeSourceOrBackendIO(t *testing.T) {
	opts, key := fixture(t)
	for _, test := range []struct {
		name       string
		encryption *runtimeconfig.ManagementEncryption
	}{
		{"unknown", &runtimeconfig.ManagementEncryption{Backend: "automatic"}},
		{"missing local", &runtimeconfig.ManagementEncryption{Backend: "local"}},
		{"conflicting", &runtimeconfig.ManagementEncryption{Local: &runtimeconfig.ManagementLocalKeys{ActiveKeyFile: key.Path}, Transit: &runtimeconfig.ManagementTransit{}}},
		{"inside state", &runtimeconfig.ManagementEncryption{Local: &runtimeconfig.ManagementLocalKeys{ActiveKeyFile: filepath.Join(opts.DataDirectory, "key")}}},
		{"relative", &runtimeconfig.ManagementEncryption{Local: &runtimeconfig.ManagementLocalKeys{ActiveKeyFile: "private-relative"}}},
		{"KMS alias", &runtimeconfig.ManagementEncryption{Backend: "aws-kms", AWSKMS: &runtimeconfig.ManagementAWSKMS{KeyARN: "arn:aws:kms:us-east-1:123456789012:alias/private", Region: "us-east-1"}}},
		{"Transit HTTP", &runtimeconfig.ManagementEncryption{Backend: "vault-transit", Transit: &runtimeconfig.ManagementTransit{Address: "http://private-token@host", Mount: "transit", Key: "cpra", TokenFile: key.Path}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts.Encryption = test.encryption
			_, err := Open(context.Background(), opts)
			if !errors.Is(err, ErrConfiguration) {
				t.Fatal(err)
			}
			if strings.Contains(err.Error(), "private-token") || strings.Contains(err.Error(), "private-relative") {
				t.Fatal("descriptor leaked")
			}
		})
	}
	if _, err := os.Stat(filepath.Dir(key.Path)); !os.IsNotExist(err) {
		t.Fatal("validation performed source IO", err)
	}
}

func TestKMSProfileParseErrorIsRedactedBeforeNetwork(t *testing.T) {
	opts, key := fixture(t)
	if _, err := secureconfig.GenerateLocalKeyFile(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key.Path, []byte("[broken\nsecret-parser-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	opts.Encryption = &runtimeconfig.ManagementEncryption{Backend: "aws-kms", AWSKMS: &runtimeconfig.ManagementAWSKMS{KeyARN: "arn:aws:kms:us-east-1:123456789012:key/12345678-1234-1234-1234-123456789012", Region: "us-east-1", Profile: "fixture", SharedConfigFiles: []string{key.Path}, SharedCredentialsFiles: []string{key.Path}}}
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	_, err := Open(context.Background(), opts)
	if !errors.Is(err, ErrBackend) {
		t.Fatal(err)
	}
	if strings.Contains(err.Error(), key.Path) || strings.Contains(err.Error(), "secret-parser-fixture") {
		t.Fatal("SDK parse error exposed source")
	}
}

func TestFinishReleasesTransportOnFailure(t *testing.T) {
	var calls int
	wrapper, err := secureconfig.NewLocalWrapper(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := finish(context.Background(), wrapper, []secureconfig.KeyWrapper{wrapper}, []func(){func() { calls++ }}); err == nil || calls != 1 {
		t.Fatal("failed keyring leaked ownership", err, calls)
	}
}
