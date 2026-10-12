package persistence

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCollectionGCDarwinIdentityRequiresLocalAPFS(t *testing.T) {
	stat := unix.Stat_t{Dev: 12, Ino: 3, Mode: unix.S_IFREG | 0600, Nlink: 1, Uid: 501,
		Btim: unix.Timespec{Sec: 1_700_000_000, Nsec: 42}}
	filesystem := unix.Statfs_t{Flags: unix.MNT_LOCAL, Fsid: unix.Fsid{Val: [2]int32{12, 34}}}
	copy(filesystem.Fstypename[:], "apfs")
	want, err := collectionGCDarwinIdentity(stat, filesystem, false, 501)
	if err != nil || want.Mount != uint64(12)<<32|34 || want.DeviceMinor != 12 || want.Inode != 3 || want.BirthNSec != 42 {
		t.Fatal("valid native identity rejected", want, err)
	}
	for _, name := range []string{"filesystem", "remote", "owner", "permission", "hardlink", "type", "birth", "mount"} {
		t.Run(name, func(t *testing.T) {
			changedStat, changedFS := stat, filesystem
			switch name {
			case "filesystem":
				clear(changedFS.Fstypename[:])
				copy(changedFS.Fstypename[:], "hfs")
			case "remote":
				changedFS.Flags = 0
			case "owner":
				changedStat.Uid++
			case "permission":
				changedStat.Mode |= 0040
			case "hardlink":
				changedStat.Nlink++
			case "type":
				changedStat.Mode = unix.S_IFLNK | 0600
			case "birth":
				changedStat.Btim = unix.Timespec{}
			case "mount":
				changedFS.Fsid = unix.Fsid{}
			}
			if _, err := collectionGCDarwinIdentity(changedStat, changedFS, false, 501); err == nil {
				t.Fatal("unverifiable identity authorized cleanup")
			}
		})
	}
	stat.Mode, stat.Nlink = unix.S_IFDIR|0700, 2
	if _, err := collectionGCDarwinIdentity(stat, filesystem, true, 501); err != nil {
		t.Fatal("private APFS directory rejected", err)
	}
	stat.Mode = unix.S_IFDIR | 0600
	if _, err := collectionGCDarwinIdentity(stat, filesystem, true, 501); !errors.Is(err, errCollectionGCBlocked) {
		t.Fatal("unsearchable directory accepted", err)
	}
}

func TestCollectionGCDarwinACLResponseRejectsAmbiguity(t *testing.T) {
	valid := make([]byte, 12)
	binary.LittleEndian.PutUint32(valid[:4], 12)
	binary.LittleEndian.PutUint32(valid[4:8], 8)
	if err := collectionGCDarwinACLResponse(valid); err != nil {
		t.Fatal("empty ACL reference rejected", err)
	}
	for _, name := range []string{"truncated", "full-size", "data", "reference", "zero"} {
		t.Run(name, func(t *testing.T) {
			raw := append([]byte(nil), valid...)
			switch name {
			case "truncated":
				raw = raw[:8]
			case "full-size":
				binary.LittleEndian.PutUint32(raw[:4], 56)
			case "data":
				binary.LittleEndian.PutUint32(raw[8:], 44)
			case "reference":
				binary.LittleEndian.PutUint32(raw[4:8], 0)
			case "zero":
				clear(raw)
			}
			if !errors.Is(collectionGCDarwinACLResponse(raw), errCollectionGCBlocked) {
				t.Fatal("ambiguous extended security accepted")
			}
		})
	}
}

func TestCollectionGCDarwinRejectsACLWithoutUnlink(t *testing.T) {
	f := newCollectionGCFixture(t)
	r := f.open(t)
	path := filepath.Join(f.dir, f.old, "ledger.db")
	// Use the native utility only to establish a real ACL fixture. All observed
	// identity, permission and cleanup behavior uses the production Go path.
	if output, err := exec.Command("/bin/chmod", "+a", "everyone allow read,write", path).CombinedOutput(); err != nil {
		t.Fatalf("establish ACL fixture: %v %s", err, output)
	}
	t.Cleanup(func() { _ = exec.Command("/bin/chmod", "-N", path).Run() })
	if _, err := r.Step(context.Background(), f.old); !errors.Is(err, errCollectionGCBlocked) {
		t.Fatal("ACL-bearing generation admitted", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("ACL-bearing database removed", err)
	}
	if _, err := os.Stat(filepath.Join(f.dir, collectionGCIntentName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unsafe candidate acquired an intent", err)
	}
	f.assertProtected(t)
}
