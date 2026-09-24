package management

import (
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func sameStartupPath(left, right string) bool {
	return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
}

func startupWindowsUser() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", ErrStageUnavailable
	}
	return user.User.Sid.String(), nil
}

func protectNewStartupDirectory(path string) error {
	owner, err := startupWindowsUser()
	if err != nil {
		return err
	}
	sddl := "O:" + owner + "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + owner + ")"
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return ErrStageUnavailable
	}
	ownerSID, _, err := sd.Owner()
	if err != nil {
		return ErrStageUnavailable
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return ErrStageUnavailable
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, ownerSID, nil, acl, nil); err != nil {
		return ErrStageUnavailable
	}
	return checkStartupDirectory(path)
}

func checkStartupDirectoryAccess(path string, _ os.FileInfo) error {
	volume := filepath.VolumeName(path)
	if len(volume) != 2 || volume[1] != ':' || strings.Contains(path[len(volume):], ":") {
		return ErrStageUnavailable
	}
	for _, component := range strings.Split(path[len(volume):], `\`) {
		if component != strings.TrimRight(component, ". ") {
			return ErrStageUnavailable
		}
	}
	current, err := startupWindowsUser()
	if err != nil {
		return err
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return ErrStageUnavailable
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		return ErrStageUnavailable
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return ErrStageUnavailable
	}
	allowed := func(sid string) bool { return sid == current || sid == "S-1-5-18" || sid == "S-1-5-32-544" }
	if !allowed(owner.String()) {
		return ErrStageUnavailable
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount == 0 {
		return ErrStageUnavailable
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var entry *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, i, &entry) != nil || entry.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return ErrStageUnavailable
		}
		sid := (*windows.SID)(unsafe.Pointer(&entry.SidStart))
		if !allowed(sid.String()) {
			return ErrStageUnavailable
		}
	}
	return nil
}

// bbolt flushes its files; FlushFileBuffers is not a directory fsync contract.
func syncStartupDirectoryEntry(string) error { return nil }
