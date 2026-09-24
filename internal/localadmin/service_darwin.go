package localadmin

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ziad-hsn/cpra/internal/installpath"
)

const launchdLabel = "io.github.ziad-hsn.cpra"

// Go's pure-Go user.Lookup does not support macOS directory services. Query the
// native account database through id, with fixed arguments and no shell.
func darwinIdentity(account string) (int, int, error) {
	if account == "" || strings.HasPrefix(account, "-") || strings.ContainsAny(account, "\r\n\x00\t /") {
		return 0, 0, errors.New("invalid service account")
	}
	ids := [2]int{}
	for i, flag := range []string{"-u", "-g"} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		output, err := exec.CommandContext(ctx, "/usr/bin/id", flag, account).Output()
		cancel()
		if err != nil {
			return 0, 0, fmt.Errorf("service account %q must already exist in the macOS account database: %w", account, err)
		}
		ids[i], err = strconv.Atoi(strings.TrimSpace(string(output)))
		if err != nil || ids[i] < 0 {
			return 0, 0, errors.New("invalid native account identifier")
		}
	}
	return ids[0], ids[1], nil
}

func nativePrepare(l installpath.Layout, account string) (string, error) {
	if l.Scope == "user" {
		if account != "" {
			return "", errors.New("LaunchAgents use the current user; omit --account")
		}
		return "", nil
	}
	if os.Geteuid() != 0 {
		return "", errors.New("system installation requires an administrator; run explicitly with sudo")
	}
	if account == "" {
		account = "_cpra"
	}
	uid, _, err := darwinIdentity(account)
	if err != nil {
		return "", err
	}
	if uid == 0 {
		return "", errors.New("CPRa LaunchDaemon requires a dedicated non-root account")
	}
	return account, nil
}

func nativeSecure(l installpath.Layout, account string) error {
	uid, gid := os.Geteuid(), os.Getegid()
	var err error
	if l.Scope == "system" {
		uid, gid, err = darwinIdentity(account)
		if err != nil {
			return err
		}
	}
	// MkdirAll(config,0700) also creates this managed common parent. The
	// daemon needs traversal to its separately protected config/state paths.
	base := filepath.Dir(l.ConfigDir)
	if info, err := os.Lstat(base); err != nil {
		return err
	} else if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("managed base must not be a symbolic link")
	}
	baseOwner, baseMode := uid, fs.FileMode(0700)
	if l.Scope == "system" {
		baseOwner, baseMode = 0, 0750
	}
	if err = os.Chown(base, baseOwner, gid); err != nil {
		return err
	}
	if err = os.Chmod(base, baseMode); err != nil {
		return err
	}
	type rule struct {
		path              string
		uid, gid          int
		dirMode, fileMode fs.FileMode
	}
	rules := []rule{{l.StateDir, uid, gid, 0700, 0600}, {l.LogDir, uid, gid, 0700, 0600}}
	if l.Scope == "system" {
		rules = append(rules, rule{l.ConfigDir, 0, gid, 0750, 0640}, rule{l.BinDir, 0, 0, 0755, 0755}, rule{l.ServiceFile, 0, 0, 0755, 0644})
	} else {
		rules = append(rules, rule{l.ConfigDir, uid, gid, 0700, 0600}, rule{l.BinDir, uid, gid, 0700, 0700}, rule{l.ServiceFile, uid, gid, 0700, 0600})
	}
	for _, rule := range rules {
		if rule.path == "" {
			continue
		}
		if _, err := os.Lstat(rule.path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		err = filepath.WalkDir(rule.path, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("refusing ownership update through link %s", path)
			}
			mode := rule.fileMode
			if entry.IsDir() {
				mode = rule.dirMode
			}
			if err := os.Chown(path, rule.uid, rule.gid); err != nil {
				return err
			}
			return os.Chmod(path, mode)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func launchdDomain(l installpath.Layout) string {
	if l.Scope == "system" {
		return "system"
	}
	return "gui/" + strconv.Itoa(os.Getuid())
}
func launchdTarget(l installpath.Layout) string { return launchdDomain(l) + "/" + launchdLabel }

type launchdStatus struct {
	loaded, running bool
	pid             int
}

func parseLaunchdStatus(output string) launchdStatus {
	status := launchdStatus{loaded: true}
	for _, line := range strings.Split(output, "\n") {
		field, value, ok := strings.Cut(strings.TrimSpace(line), " = ")
		if !ok {
			continue
		}
		switch field {
		case "state":
			status.running = value == "running"
		case "pid":
			status.pid, _ = strconv.Atoi(value)
		}
	}
	return status
}
func queryLaunchd(ctx context.Context, l installpath.Layout) (launchdStatus, error) {
	output, err := exec.CommandContext(ctx, "/bin/launchctl", "print", launchdTarget(l)).CombinedOutput()
	if ctx.Err() != nil {
		return launchdStatus{}, ctx.Err()
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && strings.Contains(string(output), "Could not find service") {
			return launchdStatus{}, nil
		}
		return launchdStatus{}, fmt.Errorf("launchctl print failed (a user agent requires an active graphical login session): %w", err)
	}
	return parseLaunchdStatus(string(output)), nil
}
func nativeRunning(ctx context.Context, l installpath.Layout) (bool, error) {
	status, err := queryLaunchd(ctx, l)
	// A loaded job can temporarily have no PID while launchd throttles a crash
	// restart. Preserve that active registration across an update. A deliberate
	// local stop uses bootout, so an intentionally stopped job remains unloaded.
	return status.loaded, err
}
func nativeRegister(ctx context.Context, l installpath.Layout, _ string) error {
	// Installing a definition does not start it. bootstrap occurs only when the
	// operator explicitly requests start, or an upgrade resumes a running job.
	status, err := queryLaunchd(ctx, l)
	if err != nil {
		return err
	}
	if status.loaded {
		if _, err = installation(l); err != nil {
			return fmt.Errorf("existing launchd service has no valid local ownership record: %w", err)
		}
	}
	return runCommand(ctx, "/usr/bin/plutil", "-lint", l.ServiceFile)
}
func nativeStart(ctx context.Context, l installpath.Layout) error {
	status, err := queryLaunchd(ctx, l)
	if err != nil {
		return err
	}
	if status.running {
		return nil
	}
	if !status.loaded {
		if err = runCommand(ctx, "/bin/launchctl", "bootstrap", launchdDomain(l), l.ServiceFile); err != nil {
			return err
		}
	} else if err = runCommand(ctx, "/bin/launchctl", "kickstart", launchdTarget(l)); err != nil {
		return err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, err = queryLaunchd(ctx, l)
		if err != nil {
			return err
		}
		if status.running {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func nativeStop(ctx context.Context, l installpath.Layout) error {
	status, err := queryLaunchd(ctx, l)
	if err != nil || !status.loaded {
		return err
	}
	if err = runCommand(ctx, "/bin/launchctl", "bootout", launchdTarget(l)); err != nil {
		return err
	}
	// bootout removes the job registration. Wait for the former owner to exit
	// before reporting stopped; backups also take the durable directory lock.
	if status.pid <= 0 {
		return nil
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		err = syscall.Kill(status.pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil && !errors.Is(err, syscall.EPERM) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func nativeUnregister(ctx context.Context, l installpath.Layout) error { return nativeStop(ctx, l) }
