package persistence

import (
	"encoding/binary"
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// APFS provides persistent file IDs and birth times. Other filesystems may
// substitute ctime for birth time; they cannot authorize this cleanup protocol.
// fstatfs supplies the mounted filesystem identity, including volume boundaries
// on the same device. This identity is local evidence, not portable Raft state.
func collectionGCNative(file *os.File, directory bool) (collectionGCIdentity, error) {
	var stat unix.Stat_t
	var filesystem unix.Statfs_t
	if unix.Fstat(int(file.Fd()), &stat) != nil || unix.Fstatfs(int(file.Fd()), &filesystem) != nil {
		return collectionGCIdentity{}, errCollectionGCUnavailable
	}
	identity, err := collectionGCDarwinIdentity(stat, filesystem, directory, uint32(os.Geteuid()))
	if err != nil {
		return collectionGCIdentity{}, err
	}
	if err := collectionGCDarwinNoACL(file); err != nil {
		return collectionGCIdentity{}, err
	}
	return identity, nil
}

func collectionGCDarwinIdentity(stat unix.Stat_t, filesystem unix.Statfs_t, directory bool, owner uint32) (collectionGCIdentity, error) {
	if unix.ByteSliceToString(filesystem.Fstypename[:]) != "apfs" || filesystem.Flags&unix.MNT_LOCAL == 0 {
		return collectionGCIdentity{}, errCollectionGCUnavailable
	}
	want := uint16(unix.S_IFREG)
	if directory {
		want = unix.S_IFDIR
	}
	if stat.Mode&unix.S_IFMT != want || stat.Mode&0077 != 0 || stat.Uid != owner ||
		stat.Ino == 0 || !directory && stat.Nlink != 1 || directory && stat.Mode&0700 != 0700 {
		return collectionGCIdentity{}, errCollectionGCBlocked
	}
	if stat.Btim.Nsec < 0 || stat.Btim.Nsec >= 1_000_000_000 {
		return collectionGCIdentity{}, errCollectionGCUnavailable
	}
	mount := uint64(uint32(filesystem.Fsid.Val[0]))<<32 | uint64(uint32(filesystem.Fsid.Val[1]))
	identity := collectionGCIdentity{DeviceMinor: uint32(stat.Dev), Mount: mount, Inode: stat.Ino,
		BirthSec: stat.Btim.Sec, BirthNSec: uint32(stat.Btim.Nsec)}
	if !collectionGCValidIdentity(identity) {
		return collectionGCIdentity{}, errCollectionGCUnavailable
	}
	return identity, nil
}

// Unlike Linux's ACL mask, a macOS ACL can grant access beyond st_mode. Read
// extended security through the already-open descriptor and reject every ACL.
// The fixed buffer represents only length plus an empty attrreference. Asking
// for full size makes even a truncated ACL distinguishable from no ACL.
// x/sys does not wrap fgetattrlist; this narrow syscall uses Apple's attrlist ABI
// and retains the descriptor and buffers until the call has completed.
func collectionGCDarwinNoACL(file *os.File) error {
	attributes := struct {
		Count, Reserved uint16
		Common          uint32
		Volume          uint32
		Directory       uint32
		File            uint32
		Fork            uint32
	}{Count: 5, Common: unix.ATTR_CMN_EXTENDED_SECURITY}
	var response [12]byte
	_, _, errno := syscall.Syscall6(unix.SYS_FGETATTRLIST, file.Fd(), uintptr(unsafe.Pointer(&attributes)),
		uintptr(unsafe.Pointer(&response[0])), uintptr(len(response)), unix.FSOPT_REPORT_FULLSIZE, 0)
	runtime.KeepAlive(file)
	runtime.KeepAlive(attributes)
	runtime.KeepAlive(response)
	if errno != 0 {
		return errCollectionGCUnavailable
	}
	return collectionGCDarwinACLResponse(response[:])
}

func collectionGCDarwinACLResponse(response []byte) error {
	if len(response) != 12 || binary.LittleEndian.Uint32(response[:4]) != 12 ||
		binary.LittleEndian.Uint32(response[8:]) != 0 {
		return errCollectionGCBlocked
	}
	// Empty variable data points at the end of the fixed response: the reference
	// starts at byte 4 and the kernel's empty variable region starts at byte 12.
	if binary.LittleEndian.Uint32(response[4:8]) != 8 {
		return errCollectionGCBlocked
	}
	return nil
}
