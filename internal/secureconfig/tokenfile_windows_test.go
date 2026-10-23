package secureconfig

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

func TestTokenFileWindowsChecksOpenedDACL(t *testing.T) {
	key := testKeyFileOptions(t)
	if _, err := GenerateLocalKeyFile(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	opts := TokenFileOptions{Path: key.Path, MaxBytes: 128}
	data, err := ReadTokenFile(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	clear(data)
	owner, err := keyUserSID()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + owner + ")(A;;FR;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(key.Path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTokenFile(t.Context(), opts); !errors.Is(err, ErrTokenFilePolicy) {
		t.Fatal("Everyone could read token", err)
	}
}
