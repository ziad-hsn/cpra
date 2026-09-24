package installpath

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestUserXDGRequiresAbsolutePaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix path semantics; Windows Known Folder resolution tested separately")
	}
	get := func(key string) string {
		if key == "XDG_STATE_HOME" {
			return "relative"
		}
		return "/configuration"
	}
	l := resolveUnix("linux", "user", "/users/alice", get)
	if l.StateDir != filepath.Join("/users/alice", ".local/state/cpra") || l.ConfigDir != "/configuration/cpra" {
		t.Fatal(l)
	}
	l = resolveUnix("linux", "system", "/users/installer", get)
	if l.StateDir != "/var/lib/cpra" || l.ConfigDir != "/etc/cpra" {
		t.Fatal(l)
	}
}

func TestMacPathsDistinguishSessionAndMachine(t *testing.T) {
	for _, scope := range []string{"user", "system"} {
		l := resolveUnix("darwin", scope, "/Users/Alice Smith", func(string) string { return "" })
		base := "/Library"
		if scope == "user" {
			base = "/Users/Alice Smith/Library"
		}
		if l.StateDir != filepath.Join(base, "Application Support/CPRa/state") {
			t.Fatal(l)
		}
	}
}
