package secureconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	keySystemSID    = "S-1-5-18"
	keyAdminsSID    = "S-1-5-32-544"
	keyInstallerSID = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"
)

func checkKeyPlatformOptions(opts KeyFileOptions) error {
	if opts.ReaderGroupID != nil {
		return fmt.Errorf("%w: Unix reader groups are unavailable on Windows", ErrKeyFile)
	}
	for _, path := range []string{opts.Path, opts.DataDirectory} {
		volume := filepath.VolumeName(path)
		if len(volume) != 2 || volume[1] != ':' || strings.Contains(path[len(volume):], ":") {
			return fmt.Errorf("%w: local Windows volume paths are required", ErrKeyFile)
		}
		for _, part := range strings.Split(path[len(volume):], `\`) {
			if part != strings.TrimRight(part, ". ") {
				return fmt.Errorf("%w: ambiguous Windows path", ErrKeyFile)
			}
		}
	}
	if opts.ReaderSID != "" {
		sid, err := windows.StringToSid(opts.ReaderSID)
		if err != nil || sid.String() != opts.ReaderSID {
			return fmt.Errorf("%w: invalid reader SID", ErrKeyFile)
		}
		switch opts.ReaderSID {
		case keySystemSID, keyAdminsSID, "S-1-1-0", "S-1-5-11", "S-1-5-32-545", "S-1-5-7":
			return fmt.Errorf("%w: reader SID must identify the intended service principal", ErrKeyFile)
		}
	}
	return nil
}

func keyPathWithin(path, parent string) bool {
	rel, err := filepath.Rel(strings.ToLower(parent), strings.ToLower(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, `..\`)
}

func keyUserSID() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", keyFileError("resolve key owner", err)
	}
	return user.User.Sid.String(), nil
}

func prepareKeyDirectory(path string, opts KeyFileOptions) error {
	if err := checkWindowsAncestors(filepath.Dir(path), opts); err != nil {
		return err
	}
	if opts.ReaderSID != "" && !windows.GetCurrentProcessToken().IsElevated() {
		return fmt.Errorf("%w: service key provisioning requires an elevated administrator", ErrKeyFile)
	}
	// CreateDirectory receives the protected DACL at creation, before the
	// directory becomes visible; there is no permissive inherited-ACL interval.
	sd, err := newKeySecurityDescriptor(opts, true)
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return keyFileError("encode key directory", err)
	}
	err = windows.CreateDirectory(name, &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd})
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return nil
	}
	if err != nil {
		return keyFileError("create protected key directory", err)
	}
	return nil
}

func openKeyFile(root *os.Root, name string) (*os.File, error) {
	return root.Open(name)
}

func newKeySecurityDescriptor(opts KeyFileOptions, directory bool) (*windows.SECURITY_DESCRIPTOR, error) {
	owner, err := keyUserSID()
	if err != nil {
		return nil, err
	}
	if opts.ReaderSID != "" {
		owner = keyAdminsSID
	}
	flags := ""
	if directory {
		flags = "OICI"
	}
	sddl := "O:" + owner + "D:P(A;" + flags + ";FA;;;SY)(A;" + flags + ";FA;;;BA)"
	if opts.ReaderSID == "" {
		sddl += "(A;" + flags + ";FA;;;" + owner + ")"
	} else {
		sddl += "(A;" + flags + ";FRFX;;;" + opts.ReaderSID + ")"
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, keyFileError("construct key protection", err)
	}
	return sd, nil
}

func protectNewKey(file *os.File, path string, opts KeyFileOptions) error {
	sd, err := newKeySecurityDescriptor(opts, false)
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return keyFileError("read key protection", err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return keyFileError("read key owner", err)
	}
	// os.OpenFile does not request WRITE_DAC/WRITE_OWNER. Reopen by name inside
	// the already protected directory, before writing any key material, then
	// verify that the original handle still refers to this protected file.
	err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, owner, nil, acl, nil)
	if err != nil {
		return keyFileError("protect new key", err)
	}
	opened, err := file.Stat()
	if err != nil {
		return keyFileError("inspect private temporary key", err)
	}
	current, err := os.Lstat(path)
	if err != nil {
		return keyFileError("inspect protected temporary key", err)
	}
	if !os.SameFile(opened, current) || !current.Mode().IsRegular() {
		return ErrKeyFileChanged
	}
	return nil
}

func checkKeyHandle(file *os.File, opts KeyFileOptions) error {
	if err := checkProtectedHandle(file, opts); err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		return keyFileError("inspect key", err)
	}
	if !info.Mode().IsRegular() || info.Size() != keySize {
		return fmt.Errorf("%w: key must be a regular 32-byte file", ErrKeyFile)
	}
	return nil
}

func checkProtectedHandle(file *os.File, opts KeyFileOptions) error {
	info, err := file.Stat()
	if err != nil {
		return keyFileError("inspect protected file", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: source must be a regular file", ErrKeyFile)
	}
	var native windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &native); err != nil {
		return keyFileError("inspect key identity", err)
	}
	if native.NumberOfLinks != 1 || native.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("%w: key aliases must resolve to a single-link regular file", ErrKeyFile)
	}
	sd, err := windows.GetSecurityInfo(windows.Handle(file.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return keyFileError("inspect key protection", err)
	}
	return checkKeySecurityDescriptor(sd, opts, true)
}

func checkKeyDirectories(opts KeyFileOptions, resolved string) error {
	sd, err := windows.GetNamedSecurityInfo(filepath.Dir(resolved), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return keyFileError("inspect key directory protection", err)
	}
	if err := checkKeySecurityDescriptor(sd, opts, true); err != nil {
		return err
	}
	if err := checkWindowsAncestors(filepath.Dir(resolved), opts); err != nil {
		return err
	}
	if err := checkWindowsAncestors(filepath.Dir(opts.Path), opts); err != nil {
		return err
	}
	if _, err := os.Lstat(opts.Path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	directories, err := aliasDirectories(opts.Path)
	if err != nil {
		return err
	}
	for _, dir := range directories {
		if err := checkWindowsAncestors(dir, opts); err != nil {
			return err
		}
	}
	return nil
}

func checkWindowsAncestors(path string, opts KeyFileOptions) error {
	for {
		sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			return keyFileError("inspect key path protection", err)
		}
		if err := checkKeySecurityDescriptor(sd, opts, false); err != nil {
			return err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return nil
		}
		path = parent
	}
}

func checkKeySecurityDescriptor(sd *windows.SECURITY_DESCRIPTOR, opts KeyFileOptions, strict bool) error {
	current, err := keyUserSID()
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return fmt.Errorf("%w: key path ownership unavailable", ErrKeyFile)
	}
	trusted := func(sid string) bool {
		return sid == keySystemSID || sid == keyAdminsSID || (!strict && sid == keyInstallerSID) || (opts.ReaderSID == "" && sid == current)
	}
	if !trusted(owner.String()) {
		return fmt.Errorf("%w: key path has an untrusted owner", ErrKeyFile)
	}
	control, _, err := sd.Control()
	if err != nil || (strict && control&windows.SE_DACL_PROTECTED == 0) {
		return fmt.Errorf("%w: key requires a protected DACL", ErrKeyFile)
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		return fmt.Errorf("%w: key path requires an explicit DACL", ErrKeyFile)
	}
	var readerAccess windows.ACCESS_MASK
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return keyFileError("inspect key access rule", err)
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE || ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return fmt.Errorf("%w: unsupported key access rule", ErrKeyFile)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if trusted(sid) {
			continue
		}
		if strict && sid == opts.ReaderSID {
			const readOnly = windows.ACCESS_MASK(windows.FILE_GENERIC_READ | windows.FILE_GENERIC_EXECUTE | windows.GENERIC_READ | windows.GENERIC_EXECUTE)
			if ace.Mask & ^readOnly != 0 {
				return fmt.Errorf("%w: service identity may only read the key", ErrKeyFile)
			}
			readerAccess |= ace.Mask
			continue
		}
		// Public ancestor traversal and child creation do not permit replacing
		// a protected owned child. DELETE_CHILD, ownership/ACL changes and
		// generic mutation grants do, and are rejected even on ancestors.
		const replaceRights = windows.ACCESS_MASK(windows.GENERIC_ALL | windows.GENERIC_WRITE | windows.WRITE_DAC | windows.WRITE_OWNER | windows.DELETE | 0x40)
		if strict || ace.Mask&replaceRights != 0 {
			return fmt.Errorf("%w: key path permits untrusted access", ErrKeyFile)
		}
	}
	if strict && opts.ReaderSID != "" && readerAccess&windows.ACCESS_MASK(windows.FILE_READ_DATA|windows.GENERIC_READ) == 0 {
		return fmt.Errorf("%w: configured service reader has no key read access", ErrKeyFile)
	}
	return nil
}

func publishKeyFile(root *os.Root, temporary, target string) error {
	if err := verifyKeyDirectory(root, root.Name()); err != nil {
		return err
	}
	source, err := windows.UTF16PtrFromString(filepath.Join(root.Name(), temporary))
	if err != nil {
		return err
	}
	destination, err := windows.UTF16PtrFromString(filepath.Join(root.Name(), target))
	if err != nil {
		return err
	}
	// Same-volume move with write-through, deliberately without REPLACE_EXISTING.
	// A directory File.Sync is not supported by the Windows filesystem API.
	return windows.MoveFileEx(source, destination, windows.MOVEFILE_WRITE_THROUGH)
}
