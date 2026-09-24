package localadmin

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/internal/installpath"
	"github.com/ziad-hsn/cpra/internal/persistence"
	"github.com/ziad-hsn/cpra/internal/runtimeconfig"
)

func TestInitRequiresExistingStoreToBeStopped(t *testing.T) {
	l := testLayout(t)
	if err := Init(l, ""); err != nil {
		t.Fatal(err)
	}
	c := runtimeconfig.Default()
	c.Storage.Directory = l.StateDir
	store, err := persistence.Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	tokenPath := filepath.Join(l.ConfigDir, "auth.token")
	before, err := os.ReadFile(tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := Init(l, ""); err == nil {
		t.Fatal("initialization admitted a running durable store")
	}
	after, err := os.ReadFile(tokenPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("failed initialization changed the token: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Init(l, ""); err != nil {
		t.Fatalf("initialization of a stopped store: %v", err)
	}
}

func testLayout(t *testing.T) installpath.Layout {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return installpath.Layout{Scope: "user", ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state"), BinDir: filepath.Join(root, "bin"), LogDir: filepath.Join(root, "logs"), ServiceFile: filepath.Join(root, "service")}
}

func TestInitPreservesOperatorFilesAndResolvesState(t *testing.T) {
	l := testLayout(t)
	if err := Init(l, ""); err != nil {
		t.Fatal(err)
	}
	c, err := runtimeconfig.Load(filepath.Join(l.ConfigDir, "runtime.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Storage.Directory != l.StateDir {
		t.Fatal(c)
	}
	token, err := os.ReadFile(filepath.Join(l.ConfigDir, "auth.token"))
	if err != nil || len(strings.TrimSpace(string(token))) != 64 {
		t.Fatalf("token: %v", err)
	}
	custom := []byte("monitors: [] # operator edited\n")
	if err = os.WriteFile(filepath.Join(l.ConfigDir, "monitors.yaml"), custom, 0600); err != nil {
		t.Fatal(err)
	}
	if err = Init(l, ""); err != nil {
		t.Fatal(err)
	}
	again, _ := os.ReadFile(filepath.Join(l.ConfigDir, "auth.token"))
	if string(token) != string(again) {
		t.Fatal("token was rotated implicitly")
	}
	again, _ = os.ReadFile(filepath.Join(l.ConfigDir, "monitors.yaml"))
	if string(custom) != string(again) {
		t.Fatal("operator configuration overwritten")
	}
}

func TestServiceRenderingUsesExplicitPathsAndShutdown(t *testing.T) {
	l := testLayout(t)
	l.BinDir = filepath.Join(l.BinDir, "spaces $and%specifiers")
	s, err := Render(l, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, "45s") || !strings.Contains(s, "-data-dir") {
		t.Fatal(s)
	}
	if runtime.GOOS == "linux" {
		for _, want := range []string{"Type=notify", "KillMode=mixed", "TimeoutStopSec=60s", "$$and%%specifiers", "WantedBy=default.target"} {
			if !strings.Contains(s, want) {
				t.Fatalf("missing %s in %s", want, s)
			}
		}
		if strings.Contains(s, "User=") {
			t.Fatal("user service forced system account")
		}
	}
	if _, err = Render(l, "bad\nExecStart=evil"); err == nil {
		t.Fatal("account injected service directives")
	}
}

func TestLocalInstallerRefusesUnownedFiles(t *testing.T) {
	l := testLayout(t)
	if err := os.MkdirAll(l.BinDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binaryPath(l), []byte("package managed"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := refuseForeignInstallation(l); err == nil {
		t.Fatal("unowned binary replacement accepted")
	}
}
