package localadmin

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ziad-hsn/cpra/internal/installpath"
)

func nativePrepare(l installpath.Layout, account string) (string, error) {
	if l.Scope == "user" {
		if account != "" {
			return "", fmt.Errorf("user services use the current user; omit --account")
		}
		return "", nil
	}
	if os.Geteuid() != 0 {
		return "", fmt.Errorf("system installation requires an administrator; run explicitly with sudo")
	}
	if account == "" {
		account = "cpra"
	}
	if strings.ContainsAny(account, "\r\n\x00\t /") || strings.HasPrefix(account, "-") {
		return "", fmt.Errorf("invalid service account")
	}
	u, err := user.Lookup(account)
	if err != nil {
		if _, ok := err.(user.UnknownUserError); !ok {
			return "", err
		}
		if account != "cpra" {
			return "", fmt.Errorf("explicit service account must already exist: %s", account)
		}
		if err = runCommand(context.Background(), "useradd", "--system", "--user-group", "--home-dir", l.StateDir, "--no-create-home", "--shell", "/usr/sbin/nologin", account); err != nil {
			return "", err
		}
		u, err = user.Lookup(account)
		if err != nil {
			return "", err
		}
	}
	if u.Uid == "0" {
		return "", fmt.Errorf("CPRa service must use a non-root account")
	}
	g, err := user.LookupGroup(account)
	if err != nil {
		return "", fmt.Errorf("matching service group required: %w", err)
	}
	if g.Gid != u.Gid {
		return "", fmt.Errorf("service account primary group must match its name")
	}
	return account, nil
}

func nativeSecure(l installpath.Layout, account string) error {
	if l.Scope == "user" {
		return nil
	}
	u, err := user.Lookup(account)
	if err != nil {
		return err
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return err
	}
	for _, dir := range []string{l.StateDir, l.LogDir} {
		if dir == "" {
			continue
		}
		// Installer operations hold the stopped-store lock. Repair ownership
		// after an administrator restores a private backup; normal starts do
		// not walk or change the data directory.
		err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("refusing ownership update through link: %s", path)
			}
			if !entry.IsDir() && !entry.Type().IsRegular() {
				return fmt.Errorf("non-regular state file: %s", path)
			}
			if err := os.Chown(path, uid, gid); err != nil {
				return err
			}
			mode := fs.FileMode(0600)
			if entry.IsDir() {
				mode = 0700
			}
			return os.Chmod(path, mode)
		})
		if err != nil {
			return err
		}
	}
	if err = os.Chown(l.ConfigDir, 0, gid); err != nil {
		return err
	}
	if err = os.Chmod(l.ConfigDir, 0750); err != nil {
		return err
	}
	for _, name := range []string{"monitors.yaml", "runtime.yaml", "auth.token"} {
		path := filepath.Join(l.ConfigDir, name)
		if _, err = os.Lstat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if err = os.Chown(path, 0, gid); err != nil {
			return err
		}
		if err = os.Chmod(path, 0640); err != nil {
			return err
		}
	}
	// Only administrator-managed executable and service paths are changed;
	// never recursively chown an existing data directory during restart.
	for _, path := range []string{l.BinDir, binaryPath(l), l.ServiceFile} {
		if _, err = os.Lstat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if err = os.Chown(path, 0, 0); err != nil {
			return err
		}
	}
	return nil
}

func systemctlArgs(l installpath.Layout, args ...string) []string {
	if l.Scope == "user" {
		return append([]string{"--user"}, args...)
	}
	return args
}
func nativeRunning(ctx context.Context, l installpath.Layout) (bool, error) {
	cmd := exec.CommandContext(ctx, "systemctl", systemctlArgs(l, "show", "--property=ActiveState", "--value", "cpra.service")...)
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("query systemd: %w", err)
	}
	switch strings.TrimSpace(string(out)) {
	case "active", "activating", "reloading":
		return true, nil
	case "inactive", "failed":
		return false, nil
	default:
		return false, fmt.Errorf("service is transitioning; wait before changing installation: %s", strings.TrimSpace(string(out)))
	}
}
func nativeRegister(ctx context.Context, l installpath.Layout, _ string) error {
	return runCommand(ctx, "systemctl", systemctlArgs(l, "daemon-reload")...)
}
func nativeStop(ctx context.Context, l installpath.Layout) error {
	return runCommand(ctx, "systemctl", systemctlArgs(l, "stop", "cpra.service")...)
}
func nativeStart(ctx context.Context, l installpath.Layout) error {
	return runCommand(ctx, "systemctl", systemctlArgs(l, "start", "cpra.service")...)
}
func nativeUnregister(ctx context.Context, l installpath.Layout) error {
	if err := runCommand(ctx, "systemctl", systemctlArgs(l, "disable", "cpra.service")...); err != nil {
		return err
	}
	if err := os.Remove(l.ServiceFile); err != nil && !os.IsNotExist(err) {
		return err
	}
	return runCommand(ctx, "systemctl", systemctlArgs(l, "daemon-reload")...)
}
