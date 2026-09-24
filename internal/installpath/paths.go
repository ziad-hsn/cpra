// Package installpath resolves persistent installation paths without creating them.
package installpath

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

type Layout struct {
	Scope       string `json:"scope"`
	ConfigDir   string `json:"config_dir"`
	StateDir    string `json:"state_dir"`
	LogDir      string `json:"log_dir"`
	BinDir      string `json:"bin_dir"`
	ServiceFile string `json:"service_file,omitempty"`
}

func Resolve(scope string) (Layout, error) {
	if scope != "user" && scope != "system" {
		return Layout{}, fmt.Errorf("scope must be user or system")
	}
	home, err := os.UserHomeDir()
	if err != nil && scope == "user" {
		return Layout{}, err
	}
	if runtime.GOOS == "windows" {
		return windowsLayout(scope)
	}
	return resolveUnix(runtime.GOOS, scope, home, os.Getenv), nil
}

func resolveUnix(goos, scope, home string, getenv func(string) string) Layout {
	l := Layout{Scope: scope}
	if goos == "darwin" {
		base := "/Library"
		if scope == "user" {
			base = filepath.Join(home, "Library")
		}
		l.ConfigDir = filepath.Join(base, "Application Support", "CPRa", "config")
		l.StateDir = filepath.Join(base, "Application Support", "CPRa", "state")
		l.LogDir = filepath.Join(base, "Logs", "CPRa")
		l.BinDir = filepath.Join(base, "Application Support", "CPRa", "bin")
		sub := "LaunchDaemons"
		if scope == "user" {
			sub = "LaunchAgents"
		}
		l.ServiceFile = filepath.Join(base, sub, "io.github.ziad-hsn.cpra.plist")
		return l
	}
	if scope == "system" {
		l.ConfigDir, l.StateDir, l.BinDir = "/etc/cpra", "/var/lib/cpra", "/usr/local/lib/cpra"
		l.ServiceFile = "/etc/systemd/system/cpra.service"
		return l
	}
	xdg := func(key, fallback string) string {
		if v := getenv(key); filepath.IsAbs(v) {
			return v
		}
		return filepath.Join(home, fallback)
	}
	l.ConfigDir = filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), "cpra")
	l.StateDir = filepath.Join(xdg("XDG_STATE_HOME", ".local/state"), "cpra")
	l.BinDir = filepath.Join(home, ".local", "lib", "cpra")
	l.ServiceFile = filepath.Join(xdg("XDG_CONFIG_HOME", ".config"), "systemd", "user", "cpra.service")
	return l
}
