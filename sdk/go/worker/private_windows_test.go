//go:build externaljobs && windows

package worker

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func assertWindowsPrivateOwner(t *testing.T, path string, directory bool) string {
	t.Helper()
	if err := checkPrivate(path, directory); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatal("new worker object did not protect its DACL", err)
	}
	return sd.String()
}

func TestWindowsPrivateJournalOwnership(t *testing.T) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	var size uint32
	err = windows.GetTokenInformation(token, windows.TokenOwner, nil, 0, &size)
	if err != windows.ERROR_INSUFFICIENT_BUFFER || size < uint32(unsafe.Sizeof(uintptr(0))) || size > 16<<10 {
		t.Fatal("cannot inspect default token owner", err)
	}
	buffer := make([]byte, size)
	if err := windows.GetTokenInformation(token, windows.TokenOwner, &buffer[0], size, &size); err != nil {
		t.Fatal(err)
	}
	defaultOwner := *(**windows.SID)(unsafe.Pointer(&buffer[0]))
	t.Logf("default object owner differs from process user: %t", !defaultOwner.Equals(user.User.Sid))
	c, err := testConfig(t).validate()
	if err != nil {
		t.Fatal(err)
	}
	j, err := openJournalFormat(c, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	defer j.close()
	assertWindowsPrivateOwner(t, c.StateDir, true)
	assertWindowsPrivateOwner(t, c.WrappingKeyPath, false)
	assertWindowsPrivateOwner(t, j.path, false)
	if err = j.close(); err != nil {
		t.Fatal(err)
	}
	j, err = openJournalFormat(c, 2, true)
	if err != nil {
		t.Fatal("reopen original journal", err)
	}
	if err = j.close(); err != nil {
		t.Fatal(err)
	}
	backup, err := ConvertJournal(t.Context(), c)
	if err != nil {
		t.Fatal("convert journal", err)
	}
	assertWindowsPrivateOwner(t, backup, false)
	j, err = openJournal(c)
	if err != nil {
		t.Fatal("open converted journal", err)
	}
	if err = j.close(); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsPrivateCreationPreservesExistingFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := makePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "existing")
	want := []byte("original bytes")
	writePrivateTestFile(t, path, want)
	before := assertWindowsPrivateOwner(t, path, false)
	file, err := createPrivateFile(path)
	if file != nil {
		_ = file.Close()
		t.Fatal("exclusive creation opened an existing file")
	}
	if !os.IsExist(err) {
		t.Fatal("exclusive creation did not reject an existing file", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) || assertWindowsPrivateOwner(t, path, false) != before {
		t.Fatal("exclusive creation changed existing bytes or security", err)
	}
}

func TestWindowsRestrictiveACL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	if err := makePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivate(dir, true); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "key")
	writePrivateTestFile(t, file, make([]byte, 32))
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
