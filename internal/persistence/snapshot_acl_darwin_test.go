package persistence

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSnapshotACLVolumeRequiresAvailableMetadata(t *testing.T) {
	var valid [44]byte
	binary.NativeEndian.PutUint32(valid[:4], 44)
	binary.NativeEndian.PutUint32(valid[4:8], unix.ATTR_CMN_EXTENDED_SECURITY)
	if err := snapshotACLVolume(valid[:]); err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{nil, valid[:43], make([]byte, 44), append(valid[:4:4], make([]byte, 40)...)} {
		if err := snapshotACLVolume(data); err == nil {
			t.Fatal("unavailable extended-security metadata accepted")
		}
	}
}

func TestSnapshotProtectionRejectsDarwinACLGrants(t *testing.T) {
	for _, inherited := range []bool{false, true} {
		parent := t.TempDir()
		path := filepath.Join(parent, "snapshots")
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		grantPath, rule := path, "everyone allow read,readattr,readextattr"
		if inherited {
			grantPath, rule = parent, rule+",directory_inherit,file_inherit"
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}
		if out, err := exec.Command("/bin/chmod", "+a", rule, grantPath).CombinedOutput(); err != nil {
			t.Fatalf("ACL fixture unavailable: %v: %s", err, out)
		}
		if inherited {
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
		}
		if err := protectSnapshotPath(path, true); err == nil {
			t.Fatal("mode bits concealed an extended ACL grant")
		}
	}
}
