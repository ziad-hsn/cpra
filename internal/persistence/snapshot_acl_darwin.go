package persistence

import (
	"encoding/binary"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

func snapshotAccessACL(file *os.File) error {
	// An empty ACL is evidence only when the volume can report extended
	// security. Otherwise unavailable metadata could look like no grants.
	attributes := unix.Attrlist{Bitmapcount: unix.ATTR_BIT_MAP_COUNT, Volattr: unix.ATTR_VOL_INFO | unix.ATTR_VOL_ATTRIBUTES}
	var volume [44]byte // length followed by two attribute_set_t records
	connection, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var errno syscall.Errno
	if err := connection.Control(func(fd uintptr) {
		for {
			_, _, errno = syscall.Syscall6(unix.SYS_FGETATTRLIST, fd, uintptr(unsafe.Pointer(&attributes)),
				uintptr(unsafe.Pointer(&volume[0])), uintptr(len(volume)), unix.FSOPT_REPORT_FULLSIZE, 0)
			if errno != syscall.EINTR {
				break
			}
		}
	}); err != nil {
		return err
	}
	if errno != 0 {
		return errno
	}
	if err := snapshotACLVolume(volume[:]); err != nil {
		return err
	}
	// Darwin extended ACLs can grant access beyond the mode bits. Keep the
	// existing descriptor-based, fail-closed ACL check used for private storage.
	return collectionGCDarwinNoACL(file)
}

func snapshotACLVolume(volume []byte) error {
	if len(volume) != 44 || binary.NativeEndian.Uint32(volume[:4]) != 44 ||
		binary.NativeEndian.Uint32(volume[4:8])&unix.ATTR_CMN_EXTENDED_SECURITY == 0 {
		return errSnapshotPermissions
	}
	return nil
}
