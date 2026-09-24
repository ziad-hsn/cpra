//go:build externaljobs && windows

package worker

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsRestrictiveACL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := makePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivate(dir, true); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "key")
	if err := os.WriteFile(file, make([]byte, 32), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivate(file, false); err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FR;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err = windows.SetNamedSecurityInfo(file, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	if err = checkPrivate(file, false); err == nil {
		t.Fatal("world-readable wrapping key accepted")
	}
}
