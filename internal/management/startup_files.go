package management

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

// startupIdentity binds a local operation to an opened filesystem object. It is
// never a portable Raft identity; inspection must be repeated after relocation.
type startupIdentity struct {
	DeviceMajor uint32
	DeviceMinor uint32
	Mount       uint64
	Inode       uint64
	BirthSec    int64
	BirthNSec   uint32
}

func startupValidIdentity(id startupIdentity) bool {
	return id.Inode != 0 && id.Mount != 0 && id.BirthSec > 0 && id.BirthNSec < 1_000_000_000
}

// The state directory may be readable/traversable by other users; it must not
// be writable by them. The newly owned stage and its database remain private.
func checkStartupParentPath(path string) error {
	if !filepath.IsAbs(path) {
		return ErrValidation
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrStageUnavailable
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !sameStartupPath(path, resolved) {
		return ErrStageUnavailable
	}
	return nil
}

func startupNative(file *os.File, directory bool) (startupIdentity, error) {
	return startupNativeMode(file, directory, true)
}

type startupFiles struct {
	parentRoot, root              *os.Root
	parent, directory, file       *os.File
	parentID, directoryID, fileID startupIdentity
	name                          string
}

func openStartupFiles(dataDirectory, name string) (files *startupFiles, err error) {
	if name != startupStageDirectory && name != startupLoadingDirectory {
		return nil, ErrValidation
	}
	if checkStartupParentPath(dataDirectory) != nil || checkStartupDirectory(filepath.Join(dataDirectory, name)) != nil {
		return nil, ErrStageUnavailable
	}
	f := &startupFiles{name: name}
	defer func() {
		if err != nil {
			_ = f.Close()
		}
	}()
	if f.parentRoot, err = os.OpenRoot(dataDirectory); err != nil {
		return nil, ErrStageUnavailable
	}
	if f.parent, err = startupOpenDirectory(f.parentRoot); err != nil {
		return nil, ErrStageUnavailable
	}
	if f.parentID, err = startupNativeMode(f.parent, true, false); err != nil {
		return nil, err
	}
	if f.root, err = f.parentRoot.OpenRoot(name); err != nil {
		return nil, ErrStageUnavailable
	}
	if f.directory, err = startupOpenDirectory(f.root); err != nil {
		return nil, ErrStageUnavailable
	}
	if f.directoryID, err = startupNative(f.directory, true); err != nil {
		return nil, err
	}
	if f.file, err = startupOpenRegular(f.root, stageFileName, true); err != nil {
		return nil, ErrStageUnavailable
	}
	if f.fileID, err = startupNative(f.file, false); err != nil {
		return nil, err
	}
	if err = f.check(false); err != nil {
		return nil, err
	}
	return f, nil
}

func (f *startupFiles) Close() error {
	if f == nil {
		return nil
	}
	var err error
	for _, file := range []*os.File{f.file, f.directory, f.parent} {
		if file != nil {
			err = errors.Join(err, file.Close())
		}
	}
	if f.root != nil {
		err = errors.Join(err, f.root.Close())
	}
	if f.parentRoot != nil {
		err = errors.Join(err, f.parentRoot.Close())
	}
	f.file, f.directory, f.parent, f.root, f.parentRoot = nil, nil, nil, nil, nil
	return err
}

// check authenticates paths against live handles, rejects extra entries and
// hard links, and bounds enumeration to two entries. An empty directory is only
// permitted after this operation removed its exact database.
func (f *startupFiles) check(empty bool) error {
	if f == nil || f.parentRoot == nil || f.root == nil {
		return ErrStageUnavailable
	}
	parent, err := os.OpenRoot(f.parentRoot.Name())
	if err != nil {
		return ErrStageUnavailable
	}
	defer parent.Close()
	p, err := startupOpenDirectory(parent)
	if err != nil {
		return ErrStageUnavailable
	}
	defer p.Close()
	if id, err := startupNativeMode(p, true, false); err != nil || id != f.parentID {
		return ErrStageUnavailable
	}
	info, err := parent.Lstat(f.name)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrStageUnavailable
	}
	root, err := parent.OpenRoot(f.name)
	if err != nil {
		return ErrStageUnavailable
	}
	defer root.Close()
	dir, err := startupOpenDirectory(root)
	if err != nil {
		return ErrStageUnavailable
	}
	defer dir.Close()
	if id, err := startupNative(dir, true); err != nil || id != f.directoryID {
		return ErrStageUnavailable
	}
	entries, readErr := dir.ReadDir(2)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return ErrStageUnavailable
	}
	if empty {
		if len(entries) != 0 {
			return ErrStageUnavailable
		}
		return nil
	}
	if len(entries) != 1 || entries[0].Name() != stageFileName || !entries[0].Type().IsRegular() {
		return ErrStageUnavailable
	}
	file, err := startupOpenRegular(root, stageFileName, false)
	if err != nil {
		return ErrStageUnavailable
	}
	defer file.Close()
	if id, err := startupNative(file, false); err != nil || id != f.fileID {
		return ErrStageUnavailable
	}
	return nil
}

// remove is only called with exclusive Store ownership and a verified loading
// stage (or a newly created, never-admitted stage). It never recurses.
func (f *startupFiles) remove() error {
	if err := f.check(false); err != nil {
		return err
	}
	if err := startupRemove(f.root, f.directory, stageFileName, f.fileID, false); err != nil {
		return ErrStageUnavailable
	}
	if f.file != nil {
		if err := f.file.Close(); err != nil {
			return ErrStageUnavailable
		}
		f.file = nil
	}
	if err := startupDirectoryCheckpoint(f.directory); err != nil {
		return ErrStageUnavailable
	}
	if err := f.check(true); err != nil {
		return err
	}
	if err := f.directory.Close(); err != nil {
		return ErrStageUnavailable
	}
	f.directory = nil
	if err := f.root.Close(); err != nil {
		return ErrStageUnavailable
	}
	f.root = nil
	if err := startupRemove(f.parentRoot, f.parent, f.name, f.directoryID, true); err != nil {
		return ErrStageUnavailable
	}
	if err := startupDirectoryCheckpoint(f.parent); err != nil {
		return ErrStageUnavailable
	}
	return nil
}

func (f *startupFiles) publish() error {
	if f.name != startupLoadingDirectory {
		return ErrValidation
	}
	if err := f.check(false); err != nil {
		return err
	}
	if err := f.file.Sync(); err != nil {
		return ErrStageUnavailable
	}
	if err := startupPublish(f.parent, f.parentRoot, f.name, startupStageDirectory); err != nil {
		return ErrStageUnavailable
	}
	f.name = startupStageDirectory
	if err := startupDirectoryCheckpoint(f.parent); err != nil {
		return ErrStageUnavailable
	}
	return f.check(false)
}
