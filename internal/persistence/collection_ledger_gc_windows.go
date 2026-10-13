package persistence

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const collectionGCIntentTemporary = "gc-retirement.tmp"

// Local NTFS provides the 64-bit file ID used by this private intent format.
// Network filesystems can return partial identities; ReFS needs a 128-bit ID.
// Neither is permitted to authorize this implementation's removal protocol.
func collectionGCNative(file *os.File, directory bool) (collectionGCIdentity, error) {
	handle := windows.Handle(file.Fd())
	var info windows.ByHandleFileInformation
	if windows.GetFileInformationByHandle(handle, &info) != nil {
		return collectionGCIdentity{}, errCollectionGCUnavailable
	}
	kind, err := windows.GetFileType(handle)
	if err != nil || kind != windows.FILE_TYPE_DISK {
		return collectionGCIdentity{}, errCollectionGCUnavailable
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		(info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory ||
		!directory && info.NumberOfLinks != 1 {
		return collectionGCIdentity{}, errCollectionGCBlocked
	}
	var serial, component, flags uint32
	var filesystem [32]uint16
	if windows.GetVolumeInformationByHandle(handle, nil, 0, &serial, &component, &flags, &filesystem[0], uint32(len(filesystem))) != nil ||
		windows.UTF16ToString(filesystem[:]) != "NTFS" || flags&windows.FILE_PERSISTENT_ACLS == 0 || serial != info.VolumeSerialNumber {
		return collectionGCIdentity{}, errCollectionGCUnavailable
	}
	var path [32768]uint16
	length, err := windows.GetFinalPathNameByHandle(handle, &path[0], uint32(len(path)), 0)
	if err != nil || length == 0 || length >= uint32(len(path)) {
		return collectionGCIdentity{}, errCollectionGCUnavailable
	}
	resolved := strings.TrimPrefix(windows.UTF16ToString(path[:length]), `\\?\`)
	volume := filepath.VolumeName(resolved)
	if len(volume) != 2 || volume[1] != ':' {
		return collectionGCIdentity{}, errCollectionGCUnavailable
	}
	encoded, err := windows.UTF16PtrFromString(volume + `\`)
	if err != nil || windows.GetDriveType(encoded) != windows.DRIVE_FIXED {
		return collectionGCIdentity{}, errCollectionGCUnavailable
	}
	if err := collectionGCWindowsPermissions(handle); err != nil {
		return collectionGCIdentity{}, err
	}
	creation := uint64(info.CreationTime.HighDateTime)<<32 | uint64(info.CreationTime.LowDateTime)
	identity := collectionGCIdentity{DeviceMinor: serial, Mount: uint64(serial),
		Inode:    uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow),
		BirthSec: int64(creation/10_000_000) - 11_644_473_600, BirthNSec: uint32(creation%10_000_000) * 100}
	if creation == 0 || !collectionGCValidIdentity(identity) {
		return collectionGCIdentity{}, errCollectionGCUnavailable
	}
	return identity, nil
}

func collectionGCWindowsPermissions(handle windows.Handle) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return errCollectionGCUnavailable
	}
	trusted := map[string]bool{"S-1-5-18": true, "S-1-5-32-544": true}
	principal := user.User.Sid.String()
	// Shared service accounts need their actual SCM service SID. Granting trust
	// to LocalService/NetworkService themselves would include unrelated services.
	if principal != "S-1-5-19" && principal != "S-1-5-20" {
		trusted[principal] = true
	}
	groups, err := windows.GetCurrentProcessToken().GetTokenGroups()
	if err != nil {
		return errCollectionGCUnavailable
	}
	for _, group := range groups.AllGroups() {
		value := group.Sid.String()
		if strings.HasPrefix(value, "S-1-5-80-") && value != "S-1-5-80-0" &&
			group.Attributes&windows.SE_GROUP_ENABLED != 0 && group.Attributes&windows.SE_GROUP_USE_FOR_DENY_ONLY == 0 {
			trusted[value] = true
		}
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return errCollectionGCUnavailable
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil || !trusted[owner.String()] {
		return errCollectionGCBlocked
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil || acl.AceCount == 0 || acl.AceCount > 64 {
		return errCollectionGCBlocked
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var entry *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(acl, i, &entry) != nil || entry == nil {
			return errCollectionGCBlocked
		}
		if entry.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 || entry.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if entry.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || entry.Header.AceSize < 16 {
			return errCollectionGCBlocked
		}
		sid := (*windows.SID)(unsafe.Pointer(&entry.SidStart))
		if !sid.IsValid() || sid.Len()+8 > int(entry.Header.AceSize) || !trusted[sid.String()] {
			return errCollectionGCBlocked
		}
	}
	return nil
}

func collectionGCOpenDirectory(root *os.Root) (*os.File, error) { return root.Open(".") }

func collectionGCOpenRegular(root *os.Root, name string, writable bool) (*os.File, error) {
	flags := os.O_RDONLY
	if writable {
		flags = os.O_RDWR // FlushFileBuffers requires write access.
	}
	return root.OpenFile(name, flags, 0)
}

func collectionGCDirectoryCheckpoint(directory *os.File) error {
	// Windows has no os.File.Sync directory contract. File data is explicitly
	// flushed; intent/catalog publication uses same-volume write-through moves,
	// and removal uses a checked write-through handle. Recheck the directory's
	// native permissions/identity here, rather than swallowing Access Denied.
	_, err := collectionGCNative(directory, true)
	return err
}

func collectionGCPublishIntent(r *collectionRetirement, raw []byte) error {
	file, err := r.root.OpenFile(collectionGCIntentTemporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errCollectionGCBlocked
	}
	// An incomplete temporary intent is preserved and blocks another admission.
	// Its fixed name bounds this failure's allocation to one file.
	_, err = file.Write(raw)
	if err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	if identity, err := collectionGCNative(r.dir, true); err != nil || identity != r.identity {
		return errCollectionGCBlocked
	}
	from, err := windows.UTF16PtrFromString(filepath.Join(r.root.Name(), collectionGCIntentTemporary))
	if err != nil {
		return errCollectionGCBlocked
	}
	to, err := windows.UTF16PtrFromString(filepath.Join(r.root.Name(), collectionGCIntentName))
	if err != nil {
		return errCollectionGCBlocked
	}
	// No replacement or cross-volume copy: an existing intent always wins.
	return windows.MoveFileEx(from, to, windows.MOVEFILE_WRITE_THROUGH)
}

func collectionGCRemove(_ *os.Root, parent *os.File, name string, expected collectionGCIdentity, directory bool) error {
	if name != "ledger.db" && name != "generation.json" && name != collectionGCIntentName && !collectionGCName(name) {
		return errCollectionGCBlocked
	}
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return errCollectionGCBlocked
	}
	attributes := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(parent.Fd()), ObjectName: objectName}
	attributes.Length = uint32(unsafe.Sizeof(attributes))
	options := uint32(windows.FILE_OPEN_REPARSE_POINT | windows.FILE_WRITE_THROUGH | windows.FILE_SYNCHRONOUS_IO_NONALERT)
	if directory {
		options |= windows.FILE_DIRECTORY_FILE
	} else {
		options |= windows.FILE_NON_DIRECTORY_FILE
	}
	var handle windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.DELETE|windows.SYNCHRONIZE,
		&attributes, &status, nil, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN, options, 0, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(handle), name)
	defer file.Close()
	identity, err := collectionGCNative(file, directory)
	if err != nil || identity != expected {
		return errCollectionGCBlocked
	}
	if !directory {
		if err := file.Sync(); err != nil {
			return err
		}
	}
	remove := byte(1) // FILE_DISPOSITION_INFO.DeleteFile is a native BOOLEAN.
	if err := windows.SetFileInformationByHandle(handle, windows.FileDispositionInfo, &remove, 1); err != nil {
		return err
	}
	return file.Close()
}
