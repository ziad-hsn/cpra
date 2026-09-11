package localadmin

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

func TestServiceCommandLineRoundTripsWindowsArguments(t *testing.T) {
	binary := `C:\Program Files\CPRa\cpra.exe`
	args := []string{"-yaml", `C:\config with spaces-é\monitors.yaml`, "", `ends\`, `embedded"quote`, `a\"b`}
	got, err := windows.DecomposeCommandLine(serviceCommandLine(binary, args))
	if err != nil || !reflect.DeepEqual(got, append([]string{binary}, args...)) {
		t.Fatalf("arguments changed: %#v %v", got, err)
	}
}

func TestWindowsServiceUpdatePreservesOperatorLifecycleSettings(t *testing.T) {
	for _, startType := range []uint32{mgr.StartManual, mgr.StartDisabled, mgr.StartAutomatic} {
		old := mgr.Config{StartType: startType, DelayedAutoStart: true, Dependencies: []string{"Tcpip"}, ServiceStartName: `DOMAIN\operator`, Description: "operator description"}
		got := updatedServiceConfig(old, `C:\Program Files\CPRa\cpra.exe`, []string{"-data-dir", `C:\ProgramData\CPRa\state`})
		if got.StartType != old.StartType || got.DelayedAutoStart != old.DelayedAutoStart || !reflect.DeepEqual(got.Dependencies, old.Dependencies) || got.Description != old.Description {
			t.Fatalf("update changed operator settings: %+v", got)
		}
		if got.ServiceStartName != "" || got.Password != "" {
			t.Fatal("update would overwrite the existing service account or password")
		}
		if got.BinaryPathName == "" || got.SidType != windows.SERVICE_SID_TYPE_UNRESTRICTED {
			t.Fatal("missing updated executable or service identity")
		}
	}
}
func TestServiceDACLSeparatesExecutableAndStateRights(t *testing.T) {
	principal := "S-1-5-80-1234"
	for _, writable := range []bool{false, true} {
		value := serviceDACL(principal, writable)
		sd, err := windows.SecurityDescriptorFromString(value)
		if err != nil {
			t.Fatal(err)
		}
		acl, _, err := sd.DACL()
		if err != nil || acl == nil {
			t.Fatalf("missing DACL: %v", err)
		}
		if !strings.HasPrefix(value, "D:P") || strings.Contains(value, ";;;WD") || strings.Contains(value, ";;;BU") {
			t.Fatal(value)
		}
		if strings.Contains(value, "0x1301bf") != writable {
			t.Fatal(value)
		}
	}
	if strings.Contains(serviceDACL("", false), principal) {
		t.Fatal("unregistered service got access")
	}
}
func TestNativeUserInitProtectsTokenACL(t *testing.T) {
	l := testLayout(t)
	if err := Init(l, ""); err != nil {
		t.Fatal(err)
	}
	token := filepath.Join(l.ConfigDir, "auth.token")
	sd, err := windows.GetNamedSecurityInfo(token, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := sd.Control()
	if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("token inherited permissive permissions: %s %v", sd.String(), err)
	}
	if strings.Contains(sd.String(), ";;;WD)") || strings.Contains(sd.String(), ";;;BU)") {
		t.Fatalf("token grants other users access: %s", sd.String())
	}
	if _, err = os.ReadFile(token); err != nil {
		t.Fatal(err)
	}
}
func TestWindowsServiceAccountValidation(t *testing.T) {
	for _, value := range []string{"", `NT AUTHORITY\LocalService`, `NT AUTHORITY\NetworkService`, `DOMAIN\cpra$`} {
		if _, err := normalizeServiceAccount(value); err != nil {
			t.Fatalf("%q: %v", value, err)
		}
	}
	for _, value := range []string{`LocalSystem`, `DOMAIN\ordinary`, "DOMAIN\\bad$\n", `DOMAIN\nested\name$`, `\name$`} {
		if _, err := normalizeServiceAccount(value); err == nil {
			t.Fatalf("unsupported account %q accepted", value)
		}
	}
}
