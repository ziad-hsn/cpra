package localadmin

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func TestInitializationPortPersistsForRuntimeAndService(t *testing.T) {
	for _, management := range []bool{false, true} {
		l := testLayout(t)
		var err error
		if management {
			_, err = InitManagement(t.Context(), l, ManagementInitOptions{Port: 19060})
		} else {
			err = Init(l, "", 19060)
		}
		if err != nil {
			t.Fatal(err)
		}
		config, err := runtimeconfig.Load(filepath.Join(l.ConfigDir, "runtime.yaml"))
		if err != nil || config.Web.ListenAddress() != "127.0.0.1:19060" {
			t.Fatal("runtime lost selected port", err)
		}
		args, err := serviceArgs(l)
		if err != nil || slices.Contains(args, "-web.addr") || !slices.Contains(args, filepath.Join(l.ConfigDir, "runtime.yaml")) {
			t.Fatal("service lost selected port", err, args)
		}
		if !management {
			if err := Init(l, "", 0); err != nil {
				t.Fatal("repeat initialization ignored existing port", err)
			}
			if err := Init(l, "", 19061); err == nil {
				t.Fatal("explicit different port silently accepted")
			}
		}
	}
}

func TestInitializationRejectsInvalidPortBeforeCreatingFiles(t *testing.T) {
	for _, port := range []int{-1, 65536} {
		l := testLayout(t)
		if err := Init(l, "", port); err == nil {
			t.Fatal("invalid observation port accepted")
		}
		if _, err := InitManagement(t.Context(), l, ManagementInitOptions{Port: port}); err == nil {
			t.Fatal("invalid management port accepted")
		}
		if _, err := os.Lstat(filepath.Join(l.ConfigDir, "runtime.yaml")); !os.IsNotExist(err) {
			t.Fatal("invalid port published configuration", err)
		}
	}
}

func TestInitializationDefaultPortPreservesExistingRuntime(t *testing.T) {
	l := testLayout(t)
	if err := os.MkdirAll(l.ConfigDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(l.ConfigDir, "runtime.yaml")
	data := []byte("storage:\n  mode: memory\n")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Init(l, "", 8060); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(data) {
		t.Fatal("existing runtime changed", err)
	}
	args, err := serviceArgs(l)
	if err != nil || !slices.Contains(args, "127.0.0.1:8060") {
		t.Fatal("existing service default changed", err)
	}
}
