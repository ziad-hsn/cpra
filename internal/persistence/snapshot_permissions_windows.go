package persistence

import (
	"errors"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var errSnapshotPermissions = errors.New("snapshot path must be protected for its process or service identity")

func snapshotSecurityDescriptor() (*windows.SECURITY_DESCRIPTOR, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	principal := user.User.Sid.String()
	if principal == "S-1-5-19" || principal == "S-1-5-20" {
		groups, err := token.GetTokenGroups()
		if err != nil {
			return nil, err
		}
		principal = ""
		for _, group := range groups.AllGroups() {
			sid := group.Sid.String()
			if strings.HasPrefix(sid, "S-1-5-80-") && sid != "S-1-5-80-0" && group.Attributes&windows.SE_GROUP_ENABLED != 0 && group.Attributes&windows.SE_GROUP_USE_FOR_DENY_ONLY == 0 {
				if principal != "" {
					return nil, errSnapshotPermissions
				}
				principal = sid
			}
		}
		if principal == "" {
			return nil, errSnapshotPermissions
		}
	}
	// OWNER RIGHTS prevents a shared service-account owner from receiving
	// implicit WRITE_DAC. The actual enabled service SID receives file access.
	return windows.SecurityDescriptorFromString("D:P(A;OICI;RC;;;OW)(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + principal + ")")
}

func createSnapshotDirectory(path string) error {
	sd, err := snapshotSecurityDescriptor()
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	err = windows.CreateDirectory(name, &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd})
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return nil
	}
	return err
}

func checkSnapshotDirectory(path string) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return errSnapshotPermissions
	}
	return nil
}

func protectSnapshotPath(path string, directory bool) error {
	sd, err := snapshotSecurityDescriptor()
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(name, windows.READ_CONTROL|windows.WRITE_DAC|windows.FILE_READ_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT|windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory || !directory && info.NumberOfLinks != 1 {
		return errSnapshotPermissions
	}
	return windows.SetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}
