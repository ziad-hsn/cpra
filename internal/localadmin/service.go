package localadmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/ziad-hsn/cpra/internal/installpath"
	"github.com/ziad-hsn/cpra/internal/persistence"
)

type Installation struct {
	Version      int    `json:"version"`
	Kind         string `json:"kind"`
	Binary       string `json:"binary"`
	BinarySHA256 string `json:"binary_sha256"`
	ServiceFile  string `json:"service_file"`
	Account      string `json:"account"`
}

func installation(l installpath.Layout) (Installation, error) {
	var r Installation
	data, err := os.ReadFile(filepath.Join(l.ConfigDir, "install.json"))
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(data, &r)
	if err != nil {
		return r, err
	}
	if r.Kind != "local" || r.Version != 1 {
		return r, fmt.Errorf("installation belongs to %q; use its package manager", r.Kind)
	}
	if r.Binary != binaryPath(l) || r.ServiceFile != l.ServiceFile {
		return r, fmt.Errorf("installation paths differ from selected scope")
	}
	return r, nil
}

// Install maintains a stable service executable separate from GOBIN. Updates
// preserve the supervisor's enabled state and restart only a running service.
func Install(ctx context.Context, l installpath.Layout, source, account string, update bool, authenticationKeyFile string) error {
	if runtime.GOOS == "windows" && l.Scope != "system" {
		return fmt.Errorf("Windows user mode is foreground-only; SCM installation requires --scope system")
	}
	for _, path := range []string{l.BinDir, l.ServiceFile, filepath.Join(l.ConfigDir, "install.json")} {
		if path != "" {
			if err := rejectSymlinkAncestors(path); err != nil {
				return err
			}
		}
	}
	if source == "" {
		return fmt.Errorf("--binary must name the candidate cpra executable")
	}
	source, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("candidate must be a regular executable")
	}
	record, recordErr := installation(l)
	if update {
		if recordErr != nil {
			return fmt.Errorf("update requires a local installation record: %w", recordErr)
		}
		if account != "" && account != record.Account {
			return fmt.Errorf("service account changes require a separately reviewed migration")
		}
		account = record.Account
		old, err := hashFile(record.Binary)
		if err != nil {
			return err
		}
		if old.SHA256 != record.BinarySHA256 {
			return fmt.Errorf("managed executable changed outside installer; refusing update")
		}
	} else {
		if !os.IsNotExist(recordErr) {
			if recordErr != nil {
				return recordErr
			}
			return fmt.Errorf("already installed; use service update")
		}
		if err = refuseForeignInstallation(l); err != nil {
			return err
		}
	}
	if source == binaryPath(l) {
		return fmt.Errorf("candidate must be separate from the managed service executable")
	}
	// Validate and retain the external backup authority before stopping or
	// changing a working installation. Missing keys cannot strand it stopped.
	var backupPath string
	var backupKey []byte
	if update {
		backupPath = l.StateDir + "-backup-" + time.Now().UTC().Format("20060102T150405.000000000Z")
		backupKey, err = loadBackupKey(ctx, authenticationKeyFile, l.StateDir, backupPath)
		if err != nil {
			return err
		}
		defer clear(backupKey)
	}
	account, err = nativePrepare(l, account)
	if err != nil {
		return err
	}
	if !update {
		if err = Init(l, account); err != nil {
			return err
		}
	}
	// Validation uses the candidate's compiled capabilities and creates no
	// storage or provider clients. Never print its configuration or token.
	args := append(serviceArgs(l), "-validate")
	if err = runCommand(ctx, source, args...); err != nil {
		return fmt.Errorf("candidate configuration validation: %w", err)
	}
	wasRunning := false
	if update {
		wasRunning, err = nativeRunning(ctx, l)
		if err != nil {
			return err
		}
		if err = nativeStop(ctx, l); err != nil {
			return err
		}
		if _, err = os.Stat(filepath.Join(l.StateDir, "raft.db")); err == nil {
			if err = backupWithKey(l.StateDir, backupPath, filepath.Join(l.ConfigDir, "monitors.yaml"), backupKey); err != nil {
				return fmt.Errorf("service stopped; pre-update backup failed: %w", err)
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if err = os.MkdirAll(l.BinDir, 0755); err != nil {
		return err
	}
	if err = copyExecutable(source, binaryPath(l)); err != nil {
		return err
	}
	if l.ServiceFile != "" {
		content, err := Render(l, account)
		if err != nil {
			return err
		}
		if err = os.MkdirAll(filepath.Dir(l.ServiceFile), 0755); err != nil {
			return err
		}
		if err = replaceFile(l.ServiceFile, []byte(content), 0644); err != nil {
			return err
		}
	}
	if err = nativeRegister(ctx, l, account); err != nil {
		return err
	}
	if err = secureStoppedInstallation(l, account); err != nil {
		return err
	}
	hash, err := hashFile(binaryPath(l))
	if err != nil {
		return err
	}
	record = Installation{Version: 1, Kind: "local", Binary: binaryPath(l), BinarySHA256: hash.SHA256, ServiceFile: l.ServiceFile, Account: account}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	if err = replaceFile(filepath.Join(l.ConfigDir, "install.json"), append(data, '\n'), 0600); err != nil {
		return err
	}
	if wasRunning {
		return nativeStart(ctx, l)
	}
	return nil
}

func secureStoppedInstallation(l installpath.Layout, account string) error {
	if _, err := os.Lstat(filepath.Join(l.StateDir, "raft.db")); err == nil {
		lock, err := persistence.LockOffline(l.StateDir)
		if err != nil {
			return err
		}
		defer lock.Close()
	} else if !os.IsNotExist(err) {
		return err
	}
	return nativeSecure(l, account)
}

// Uninstall removes only recorded installation files. Configuration, account,
// backups and durable state are intentionally preserved.
func Uninstall(ctx context.Context, l installpath.Layout) error {
	r, err := installation(l)
	if err != nil {
		return err
	}
	hash, err := hashFile(r.Binary)
	if err != nil {
		return err
	}
	if hash.SHA256 != r.BinarySHA256 {
		return fmt.Errorf("managed executable changed outside installer; refusing removal")
	}
	if err = nativeStop(ctx, l); err != nil {
		return err
	}
	if err = nativeUnregister(ctx, l); err != nil {
		return err
	}
	if l.ServiceFile != "" {
		if err = os.Remove(l.ServiceFile); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err = os.Remove(r.Binary); err != nil {
		return err
	}
	return os.Remove(filepath.Join(l.ConfigDir, "install.json"))
}

func refuseForeignInstallation(l installpath.Layout) error {
	paths := []string{binaryPath(l), l.ServiceFile}
	if l.Scope == "system" {
		paths = append(paths, "/usr/lib/systemd/system/cpra.service", "/lib/systemd/system/cpra.service")
	}
	for _, path := range paths {
		if path == "" {
			continue
		}
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("existing file is not owned by the local installer: %s", path)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func runCommand(ctx context.Context, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	// Provider/configuration errors can contain credentials: output remains on
	// the operator's terminal for validation, never included in install records.
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", filepath.Base(name), err)
	}
	return nil
}

func copyExecutable(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".cpra-executable-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = io.Copy(tmp, in)
	err = errors.Join(err, tmp.Chmod(0755), tmp.Sync(), tmp.Close())
	if err != nil {
		return err
	}
	return replacePath(tmp.Name(), destination)
}

func replaceFile(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".cpra-config-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	err = errors.Join(err, f.Chmod(mode), f.Sync(), f.Close())
	if err != nil {
		return err
	}
	return replacePath(f.Name(), path)
}
