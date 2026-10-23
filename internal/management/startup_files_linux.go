package management

import (
	"os"

	"golang.org/x/sys/unix"
)

// statx supplies mount identity even for same-device bind mounts. Birth time
// guards stage identities against ordinary inode reuse. Missing kernel/filesystem
// support fails closed, including when the other identity fields are available.
func startupNativeMode(file *os.File, directory, private bool) (startupIdentity, error) {
	var stat unix.Statx_t
	mask := uint32(unix.STATX_BASIC_STATS | unix.STATX_BTIME | unix.STATX_MNT_ID)
	if unix.Statx(int(file.Fd()), "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, int(mask), &stat) != nil || stat.Mask&mask != mask {
		return startupIdentity{}, ErrStageUnavailable
	}
	want := uint16(unix.S_IFREG)
	if directory {
		want = unix.S_IFDIR
	}
	maskPermissions := uint16(0022)
	if private {
		maskPermissions = 0077
	}
	if stat.Mode&unix.S_IFMT != want || stat.Mode&maskPermissions != 0 || (os.Geteuid() != 0 && stat.Uid != uint32(os.Geteuid())) ||
		stat.Ino == 0 || stat.Mnt_id == 0 || !directory && stat.Nlink != 1 || directory && stat.Mode&0700 != 0700 {
		return startupIdentity{}, ErrStageUnavailable
	}
	identity := startupIdentity{DeviceMajor: stat.Dev_major, DeviceMinor: stat.Dev_minor, Mount: stat.Mnt_id,
		Inode: stat.Ino, BirthSec: stat.Btime.Sec, BirthNSec: stat.Btime.Nsec}
	if !startupValidIdentity(identity) {
		return startupIdentity{}, ErrStageUnavailable
	}
	return identity, nil
}
