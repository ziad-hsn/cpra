package management

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestStartupWindowsDirectoryPrivateCreationAndUnsafeExistingACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private-stage")
	created, err := createStartupDirectory(path)
	if err != nil || !created {
		t.Fatal("private directory creation failed", err)
	}
	if err := checkStartupDirectory(path); err != nil {
		t.Fatal("created private directory failed verification", err)
	}
	created, err = createStartupDirectory(path)
	if err != nil || created {
		t.Fatal("existing private directory was replaced", err)
	}
	owner, err := startupWindowsUser()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("O:" + owner + "D:P(A;OICI;FA;;;" + owner + ")(A;OICI;FR;;;WD)")
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
	if _, err := createStartupDirectory(path); !errors.Is(err, ErrStageUnavailable) {
		t.Fatalf("unsafe existing ACL accepted or silently repaired: %v", err)
	}
	if err := checkStartupDirectory(path); !errors.Is(err, ErrStageUnavailable) {
		t.Fatal("rejected ACL was unexpectedly changed", err)
	}
}

func TestStartupWindowsSourceValidationUsesPrivateEncryptedStaging(t *testing.T) {
	result, err := ValidateStartupSource(context.Background(), startupTestOptions("", stageMonitor("native-windows")))
	if err != nil || result.Monitors != 1 || result.Resources != 1 {
		t.Fatalf("native Windows source validation failed: %+v %v", result, err)
	}
	path, err := temporaryStartupDirectory()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(path)
	if err := checkStartupDirectory(path); err != nil {
		t.Fatal(err)
	}
}
