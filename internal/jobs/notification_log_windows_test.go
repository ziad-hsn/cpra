//go:build windows

package jobs

import (
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"testing"
)

func setNotificationTestACL(t *testing.T, path, extra string) {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;" + user.User.Sid.String() + ")" + extra)
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
}

func TestNotificationLogWindowsRejectsPublicACL(t *testing.T) {
	parent := t.TempDir()
	setNotificationTestACL(t, parent, "")
	directory := filepath.Join(parent, "notifications")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	setNotificationTestACL(t, directory, "(A;OICI;FR;;;WD)")
	if log, err := OpenNotificationLog(directory); err == nil {
		log.Close()
		t.Fatal("public root ACL accepted")
	}
	setNotificationTestACL(t, directory, "")
	log, err := OpenNotificationLog(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	file := filepath.Join(directory, "events.log")
	if err := os.WriteFile(file, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	setNotificationTestACL(t, file, "(A;;FR;;;WD)")
	if (&CodeLogJob{Log: log, File: "events.log"}).Execute().Err == nil {
		t.Fatal("public file ACL accepted")
	}
	bytes, err := os.ReadFile(file)
	if err != nil || string(bytes) != "unchanged" {
		t.Fatal("rejected file modified", err)
	}
	setNotificationTestACL(t, file, "")
	if (&CodeLogJob{Log: log, File: "events.log"}).Execute().Err != nil {
		t.Fatal("private file rejected")
	}
}
