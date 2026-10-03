package secureconfig

import (
	"encoding/binary"
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	darwinVolumeAttributesSize = 44
	darwinACLResponseSize      = 12
	darwinFilesecHeaderSize    = 44
	darwinACESize              = 24
	darwinACLMaxEntries        = 128
	darwinFilesecMagic         = 0x012cc16d
	darwinNoACL                = ^uint32(0)
	darwinACLNoInherit         = uint32(1 << 17)
	darwinACEKindMask          = uint32(0xf)
	darwinACEDeny              = uint32(2)
	darwinACEAllowedFlags      = uint32(0x1ff)
)

func checkKeyAccessACL(file *os.File, path string) error {
	// An empty ACL response alone cannot distinguish absent ACLs from a volume
	// that cannot report them. Require the volume's extended-security support.
	volumeAttributes := unix.Attrlist{
		Bitmapcount: unix.ATTR_BIT_MAP_COUNT,
		Volattr:     unix.ATTR_VOL_INFO | unix.ATTR_VOL_ATTRIBUTES,
	}
	var volume [darwinVolumeAttributesSize]byte
	if err := readDarwinAttributes(file, path, volumeAttributes, volume[:]); err != nil {
		return keyFileError("inspect filesystem access permissions", err)
	}
	if err := checkDarwinACLVolume(volume[:]); err != nil {
		return err
	}

	attributes := unix.Attrlist{
		Bitmapcount: unix.ATTR_BIT_MAP_COUNT,
		Commonattr:  unix.ATTR_CMN_EXTENDED_SECURITY,
	}
	var buffer [darwinACLResponseSize + darwinFilesecHeaderSize + darwinACLMaxEntries*darwinACESize]byte
	if err := readDarwinAttributes(file, path, attributes, buffer[:]); err != nil {
		return keyFileError("inspect extended access permissions", err)
	}
	return checkDarwinACL(buffer[:])
}

func readDarwinAttributes(file *os.File, path string, attributes unix.Attrlist, buffer []byte) error {
	var errno syscall.Errno
	// x/sys exposes the attribute layout and syscall numbers, but no getter.
	// REPORT_FULLSIZE prevents a truncated ACL from looking like an empty one.
	if file != nil {
		connection, err := file.SyscallConn()
		if err != nil {
			return err
		}
		if err := connection.Control(func(fd uintptr) {
			for {
				_, _, errno = syscall.Syscall6(unix.SYS_FGETATTRLIST, fd,
					uintptr(unsafe.Pointer(&attributes)), uintptr(unsafe.Pointer(&buffer[0])),
					uintptr(len(buffer)), unix.FSOPT_REPORT_FULLSIZE, 0)
				if errno != syscall.EINTR {
					break
				}
			}
		}); err != nil {
			return err
		}
	} else {
		name, err := unix.BytePtrFromString(path)
		if err != nil {
			return err
		}
		for {
			_, _, errno = syscall.Syscall6(unix.SYS_GETATTRLIST, uintptr(unsafe.Pointer(name)),
				uintptr(unsafe.Pointer(&attributes)), uintptr(unsafe.Pointer(&buffer[0])),
				uintptr(len(buffer)), unix.FSOPT_REPORT_FULLSIZE, 0)
			if errno != syscall.EINTR {
				break
			}
		}
	}
	if errno != 0 {
		return errno
	}
	return nil
}

func checkDarwinACLVolume(buffer []byte) error {
	// ATTR_VOL_ATTRIBUTES is two attribute_set_t records, each with five masks.
	// The first common-attribute mask describes metadata this volume can report.
	if len(buffer) < darwinVolumeAttributesSize || binary.NativeEndian.Uint32(buffer[:4]) != darwinVolumeAttributesSize {
		return fmt.Errorf("%w: filesystem access permission metadata is incomplete or invalid", ErrKeyFile)
	}
	if binary.NativeEndian.Uint32(buffer[4:8])&unix.ATTR_CMN_EXTENDED_SECURITY == 0 {
		return fmt.Errorf("%w: filesystem cannot report extended access permissions", ErrKeyFile)
	}
	return nil
}

func checkDarwinACL(buffer []byte) error {
	invalid := func() error {
		return fmt.Errorf("%w: extended access permissions are incomplete or invalid", ErrKeyFile)
	}
	if len(buffer) < darwinACLResponseSize {
		return invalid()
	}
	order := binary.NativeEndian
	size := uint64(order.Uint32(buffer[:4]))
	offset := int32(order.Uint32(buffer[4:8]))
	length := uint64(order.Uint32(buffer[8:12]))
	// Only EXTENDED_SECURITY was requested: one length, one attrreference,
	// then the kauth_filesec payload. Offsets are relative to the reference.
	if size < darwinACLResponseSize || size > uint64(len(buffer)) || offset != 8 || length != size-darwinACLResponseSize {
		return invalid()
	}
	if length == 0 {
		return nil
	}
	if length < darwinFilesecHeaderSize {
		return invalid()
	}
	security := buffer[darwinACLResponseSize:size]
	if order.Uint32(security[:4]) != darwinFilesecMagic {
		return invalid()
	}
	count := order.Uint32(security[36:40])
	flags := order.Uint32(security[40:44])
	if flags & ^darwinACLNoInherit != 0 {
		return fmt.Errorf("%w: unsupported extended access permission flags", ErrKeyFile)
	}
	if count == darwinNoACL {
		if length != darwinFilesecHeaderSize {
			return invalid()
		}
		return nil
	}
	if count > darwinACLMaxEntries || length != darwinFilesecHeaderSize+uint64(count)*darwinACESize {
		return invalid()
	}
	for index := uint32(0); index < count; index++ {
		start := darwinFilesecHeaderSize + int(index)*darwinACESize
		entryFlags := order.Uint32(security[start+16 : start+20])
		// Denials can only restrict the checked owner/group permissions. Permit
		// entries, including inherit-only grants, could add an unapproved reader.
		if entryFlags&darwinACEKindMask != darwinACEDeny || entryFlags & ^darwinACEAllowedFlags != 0 {
			return fmt.Errorf("%w: extended access grants are unsupported for local key paths", ErrKeyFile)
		}
	}
	return nil
}
