package persistence

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestSnapshotAdministrativeOpenPreservesServiceGrant(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "snapshots")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	// A synthetic service SID tests retention of an existing grant without
	// requiring service installation or asserting native SCM execution evidence.
	const service = "S-1-5-80-111-222-333-444-555"
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;" + service + ")")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	before, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newSnapshotStore(base, 3, true); err != nil {
		t.Fatal(err)
	}
	after, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil || before.String() != after.String() {
		t.Fatal("administrative open changed the runtime service grant", err)
	}
}

func checkSnapshotPermissions(t *testing.T, path string, directory bool) {
	t.Helper()
	expected, err := snapshotSecurityDescriptor()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("snapshot DACL inherits unprotected access", err)
	}
	wanted, _, err := expected.DACL()
	if err != nil {
		t.Fatal(err)
	}
	actual, _, err := sd.DACL()
	if err != nil || actual == nil || wanted.AceCount != actual.AceCount {
		t.Fatal("snapshot DACL entries differ", err)
	}
	for n := uint32(0); n < uint32(actual.AceCount); n++ {
		var a, b *windows.ACCESS_ALLOWED_ACE
		if windows.GetAce(actual, n, &a) != nil || windows.GetAce(wanted, n, &b) != nil || a == nil || b == nil || a.Header.AceType != b.Header.AceType || a.Mask != b.Mask {
			t.Fatal("unexpected snapshot access grant")
		}
		left, right := (*windows.SID)(unsafe.Pointer(&a.SidStart)), (*windows.SID)(unsafe.Pointer(&b.SidStart))
		if !left.Equals(right) {
			t.Fatal("snapshot grants access to another principal")
		}
	}
}
