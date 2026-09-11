package runtimeconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultsAndExplicitMemoryMode(t *testing.T) {
	c, err := Load("")
	if err != nil || c.Storage.Mode != "raft" || c.Storage.Directory != "" {
		t.Fatal(c, err)
	}
	p := filepath.Join(t.TempDir(), "runtime.yaml")
	for _, test := range []struct {
		data  string
		valid bool
	}{{"storage:\n  mode: memory\n", true}, {"storage:\n  mode: typo\n", false}, {"storage:\n  no_sync: true\n", false}, {"storage:\n  batch_size: 1001\n", false}, {"history:\n  retention_days: 0\n", false}} {
		if err := os.WriteFile(p, []byte(test.data), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(p)
		if (err == nil) != test.valid {
			t.Fatal(test.data, err)
		}
	}
}

func TestDirectoryPrecedenceAndLegacyGuard(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	c := Default()
	if err := c.ResolveStorageDirectory(""); err != nil || !filepath.IsAbs(c.Storage.Directory) {
		t.Fatal(c, err)
	}
	if err := os.Mkdir("cpra-data", 0700); err != nil {
		t.Fatal(err)
	}
	c = Default()
	if err := c.ResolveStorageDirectory(""); err == nil {
		t.Fatal("silently abandoned legacy store")
	}
	c.Storage.Directory = "configured"
	if err := c.ResolveStorageDirectory("selected"); err != nil || filepath.Base(c.Storage.Directory) != "selected" {
		t.Fatal(c, err)
	}
	c = Default()
	c.Storage.Mode = "memory"
	if err := c.ResolveStorageDirectory(""); err != nil {
		t.Fatal(err)
	}
}
