//go:build windows

package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func openNotificationLogFile(root *os.Root, name string) (*os.File, error) {
	info, err := root.Lstat(name)
	if err == nil && !info.Mode().IsRegular() || err != nil && !os.IsNotExist(err) {
		return nil, errNotificationLog
	}
	return root.OpenFile(name, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
}

func checkNotificationLogFile(f *os.File) error {
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errNotificationLog
	}
	var native windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &native) != nil || native.NumberOfLinks != 1 || native.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errNotificationLog
	}
	return checkNotificationLogACL(windows.Handle(f.Fd()), false, true)
}

func openNotificationLogDirectory(root *os.Root, name string) (*os.File, error) {
	// Every ancestor has already been opened and checked for replacement rights.
	// OPEN_REPARSE_POINT inspects junctions and links instead of following them.
	path := filepath.Join(root.Name(), name)
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(pointer, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	file := os.NewFile(uintptr(handle), path)
	var native windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(handle, &native) != nil || native.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || native.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		file.Close()
		return nil, errNotificationLog
	}
	return file, nil
}

func checkNotificationLogDirectory(file *os.File, private bool) error {
	info, err := file.Stat()
	if err != nil || !info.IsDir() {
		return errNotificationLog
	}
	if err := checkNotificationLogACL(windows.Handle(file.Fd()), true, private); err != nil {
		if private {
			return errNotificationLogDirectoryAccess
		}
		return err
	}
	return nil
}

// Windows mode bits do not restrict readers. Verify the opened object's ACL;
// the root must also give private ACLs to newly inherited children.
func checkNotificationLogACL(handle windows.Handle, directory, private bool) error {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return errNotificationLog
	}
	trusted := map[string]bool{"S-1-5-18": true, "S-1-5-32-544": true}
	if !private {
		trusted["S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"] = true
	}
	principal := user.User.Sid.String()
	if principal != "S-1-5-19" && principal != "S-1-5-20" {
		trusted[principal] = true
	}
	groups, err := token.GetTokenGroups()
	if err != nil {
		return errNotificationLog
	}
	for _, group := range groups.AllGroups() {
		sid := group.Sid.String()
		if strings.HasPrefix(sid, "S-1-5-80-") && sid != "S-1-5-80-0" && group.Attributes&windows.SE_GROUP_ENABLED != 0 && group.Attributes&windows.SE_GROUP_USE_FOR_DENY_ONLY == 0 {
			trusted[sid] = true
		}
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return errNotificationLog
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !trusted[owner.String()] {
		return errNotificationLog
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount == 0 || acl.AceCount > 64 {
		return errNotificationLog
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, i, &ace) != nil || ace == nil {
			return errNotificationLog
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE || (!directory || !private) && ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceSize < 16 {
			return errNotificationLog
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !sid.IsValid() || sid.Len()+8 > int(ace.Header.AceSize) {
			return errNotificationLog
		}
		const replaceRights = windows.ACCESS_MASK(windows.GENERIC_ALL | windows.GENERIC_WRITE | windows.WRITE_DAC | windows.WRITE_OWNER | windows.DELETE | 0x40)
		if !trusted[sid.String()] && (private || ace.Mask&replaceRights != 0) {
			return errNotificationLog
		}
	}
	return nil
}
