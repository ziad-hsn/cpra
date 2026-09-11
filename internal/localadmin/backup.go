// Package localadmin implements offline administration without constructing a
// controller or invoking monitor providers.
package localadmin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ziad-hsn/cpra/internal/durable"
	"github.com/ziad-hsn/cpra/internal/version"
)

type BackupFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type BackupManifest struct {
	Version       int       `json:"version"`
	StorageFormat int       `json:"storage_format"`
	NodeID        string    `json:"node_id"`
	Created       time.Time `json:"created"`
	Artifact      string    `json:"artifact"`
	// ConfigurationDigest identifies matching configuration without copying
	// provider credentials into the backup manifest.
	ConfigurationDigest string       `json:"configuration_sha256,omitempty"`
	Files               []BackupFile `json:"files"`
}

// Backup creates a new complete directory. It holds the same exclusive bbolt
// lock as CPRa until all data has been copied, synced, and verified.
func Backup(source, destination, config string) error {
	source, destination, err := separatePaths(source, destination)
	if err != nil {
		return err
	}
	if err = requireAbsent(destination); err != nil {
		return err
	}
	lock, err := durable.LockOffline(source)
	if err != nil {
		return err
	}
	defer lock.Close()
	stage, err := stageDirectory(destination)
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	files, err := copyTree(source, filepath.Join(stage, "data"))
	if err != nil {
		return err
	}
	m := BackupManifest{Version: 1, StorageFormat: durable.FormatVersion, NodeID: lock.NodeID, Created: time.Now().UTC(), Artifact: version.Info(), Files: files}
	if config != "" {
		f, err := hashFile(config)
		if err != nil {
			return fmt.Errorf("matching configuration: %w", err)
		}
		m.ConfigurationDigest = f.SHA256
	}
	encoded, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err = writeNew(filepath.Join(stage, "BACKUP.json"), append(encoded, '\n'), 0600); err != nil {
		return err
	}
	if _, err = verifyBackup(stage); err != nil {
		return err
	}
	if err = syncDirectory(stage); err != nil {
		return err
	}
	return replacePath(stage, destination)
}

// Restore validates every file and the storage format before publishing a new
// data directory. It never overwrites an existing store or repairs corrupt data.
func Restore(backup, destination string) error {
	backup, destination, err := separatePaths(backup, destination)
	if err != nil {
		return err
	}
	if err = requireAbsent(destination); err != nil {
		return err
	}
	m, err := verifyBackup(backup)
	if err != nil {
		return err
	}
	lock, err := durable.LockOffline(filepath.Join(backup, "data"))
	if err != nil {
		return err
	}
	defer lock.Close()
	if lock.NodeID != m.NodeID {
		return fmt.Errorf("backup node identity does not match inventory")
	}
	stage, err := stageDirectory(destination)
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	// copyTree requires a new root; its parent is the private staging directory.
	copied := filepath.Join(stage, "state")
	files, err := copyTree(filepath.Join(backup, "data"), copied)
	if err != nil {
		return err
	}
	if !sameInventory(files, m.Files) {
		return fmt.Errorf("backup changed while restoring")
	}
	validation, err := durable.LockOffline(copied)
	if err != nil {
		return err
	}
	if err = validation.Close(); err != nil {
		return err
	}
	return replacePath(copied, destination)
}

func verifyBackup(root string) (BackupManifest, error) {
	var m BackupManifest
	f, err := os.Open(filepath.Join(root, "BACKUP.json"))
	if err != nil {
		return m, err
	}
	d := json.NewDecoder(io.LimitReader(f, 32<<20))
	d.DisallowUnknownFields()
	err = d.Decode(&m)
	if err == nil {
		var extra any
		if d.Decode(&extra) != io.EOF {
			err = fmt.Errorf("trailing backup inventory data")
		}
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return m, err
	}
	if m.Version != 1 || m.StorageFormat != durable.FormatVersion || m.NodeID == "" || len(m.Files) == 0 {
		return m, fmt.Errorf("incompatible or empty backup inventory")
	}
	files, err := treeInventory(filepath.Join(root, "data"))
	if err != nil {
		return m, err
	}
	if !sameInventory(files, m.Files) {
		return m, fmt.Errorf("backup inventory mismatch: missing, additional, or changed state files")
	}
	return m, nil
}

func sameInventory(a, b []BackupFile) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func separatePaths(source, destination string) (string, string, error) {
	if source == "" || destination == "" {
		return "", "", fmt.Errorf("source and destination are required")
	}
	source, err := filepath.Abs(source)
	if err != nil {
		return "", "", err
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return "", "", err
	}
	// Resolve ancestors before testing containment; symlinked parent directories
	// must not make a nominally separate backup land inside the live store.
	source, err = filepath.EvalSymlinks(source)
	if err != nil {
		return "", "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(destination))
	if err != nil {
		return "", "", err
	}
	destination = filepath.Join(parent, filepath.Base(destination))
	inside := func(a, b string) bool {
		rel, e := filepath.Rel(a, b)
		return e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	if inside(source, destination) || inside(destination, source) {
		return "", "", fmt.Errorf("source and destination must be separate non-nested directories")
	}
	return source, destination, nil
}

func requireAbsent(path string) error {
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		if err != nil {
			return err
		}
		return fmt.Errorf("destination already exists: %s", path)
	}
	return nil
}

func stageDirectory(destination string) (string, error) {
	return os.MkdirTemp(filepath.Dir(destination), ".cpra-stage-")
}

func treeInventory(root string) ([]BackupFile, error) {
	var files []BackupFile
	err := walkRegular(root, func(path, rel string, info fs.FileInfo) error {
		if info.IsDir() {
			return nil
		}
		f, err := hashFile(path)
		f.Path = filepath.ToSlash(rel)
		files = append(files, f)
		return err
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, err
}

func walkRegular(root string, visit func(string, string, fs.FileInfo) error) error {
	return filepath.Walk(root, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("state contains non-regular file: %s", path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		return visit(path, rel, info)
	})
}

func copyTree(source, destination string) ([]BackupFile, error) {
	var directories []string
	err := walkRegular(source, func(path, rel string, info fs.FileInfo) error {
		target := filepath.Join(destination, rel)
		if info.IsDir() {
			directories = append(directories, target)
			return os.Mkdir(target, 0700)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			in.Close()
			return err
		}
		_, copyErr := io.Copy(out, in)
		return errors.Join(copyErr, out.Sync(), out.Close(), in.Close())
	})
	if err != nil {
		return nil, err
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err = syncDirectory(directories[i]); err != nil {
			return nil, err
		}
	}
	return treeInventory(destination)
}

func hashFile(path string) (BackupFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return BackupFile{}, err
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	err = errors.Join(err, f.Close())
	return BackupFile{Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, err
}

func writeNew(path string, data []byte, mode fs.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	return errors.Join(err, f.Sync(), f.Close())
}
