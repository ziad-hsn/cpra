package localadmin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"unsafe"

	"github.com/ziad-hsn/cpra/internal/installpath"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
	"golang.org/x/sys/windows"
)

// New system configuration is administrator-owned from creation. Existing
// directories are inspected, never taken over from another owner.
func prepareConfigDirectory(l installpath.Layout, account string) error {
	if l.Scope != "system" {
		return nil
	}
	_, sid, err := managementServiceReader(l, account)
	if err != nil {
		return err
	}
	if err := rejectSymlinkAncestors(l.ConfigDir); err != nil {
		return err
	}
	var missing []string
	for path := l.ConfigDir; ; path = filepath.Dir(path) {
		info, err := os.Lstat(path)
		if err == nil {
			if !info.IsDir() {
				return errors.New("system configuration parent must be a directory")
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return errors.New("system configuration parent cannot be inspected")
		}
		if filepath.Dir(path) == path {
			return errors.New("system configuration parent is unavailable")
		}
		missing = append(missing, path)
	}
	descriptor, err := windows.SecurityDescriptorFromString("O:BA" + serviceDACL(sid, false))
	if err != nil {
		return errors.New("system configuration protection cannot be prepared")
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: descriptor}
	for index := len(missing) - 1; index >= 0; index-- {
		name, err := windows.UTF16PtrFromString(missing[index])
		if err != nil {
			return errors.New("system configuration path is invalid")
		}
		if err := windows.CreateDirectory(name, &attributes); err != nil {
			// An independently created path is not ours to change.
			return errors.New("system configuration directory could not be created exclusively")
		}
	}
	return checkSystemConfigOwner(l.ConfigDir)
}

func writeInitialConfig(l installpath.Layout, account, name string, data []byte) error {
	if l.Scope != "system" {
		return writeNew(filepath.Join(l.ConfigDir, name), data, 0600)
	}
	_, sid, err := managementServiceReader(l, account)
	if err != nil {
		return err
	}
	return secureconfig.WriteNewProtectedFile(context.Background(), secureconfig.ProtectedFileOptions{
		Path: filepath.Join(l.ConfigDir, name), DataDirectory: l.StateDir, ReaderSID: sid, MaxBytes: 1 << 20,
	}, data)
}

func checkSystemConfigOwner(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return errors.New("system configuration directory cannot be inspected")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.IsDir() {
		return errors.New("system configuration parent must be a directory")
	}
	current, err := os.Lstat(path)
	if err != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, current) {
		return errors.New("system configuration directory changed during inspection")
	}
	descriptor, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return errors.New("system configuration ownership cannot be inspected")
	}
	owner, _, err := descriptor.Owner()
	if err != nil || owner == nil || owner.String() != "S-1-5-18" && owner.String() != "S-1-5-32-544" {
		return errors.New("existing system configuration ownership is incompatible; use stopped administration")
	}
	return nil
}
