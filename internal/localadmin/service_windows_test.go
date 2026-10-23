package localadmin

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ziad-hsn/cpra/internal/installpath"
	"github.com/ziad-hsn/cpra/internal/secureconfig"
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
	if err := Init(l, "", 0); err != nil {
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

// This fixture never registers or starts SCM. It exercises the exact protected
// input preparation used before candidate validation during system installation.
func windowsSystemConfigFixture(t *testing.T) (installpath.Layout, string) {
	t.Helper()
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("native system configuration verification requires an elevated Windows fixture")
	}
	base, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "cpra-token-test-"+rand.Text())
	l := installpath.Layout{Scope: "system", ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state"), LogDir: filepath.Join(root, "logs"), BinDir: filepath.Join(root, "bin")}
	if err := prepareConfigDirectory(l, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error("remove owned Windows token fixture", err)
		}
	})
	for _, path := range []string{l.StateDir, l.LogDir} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	_, sid, err := managementServiceReader(l, "")
	if err != nil {
		t.Fatal(err)
	}
	return l, sid
}

func tokenDescriptor(t *testing.T, path string) string {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	return sd.String()
}

func TestWindowsSystemTokenValidatesBeforeServiceRegistration(t *testing.T) {
	l, sid := windowsSystemConfigFixture(t)
	if err := nativeSecure(l, ""); err != nil {
		t.Fatal(err)
	}
	token := []byte("pre-registration-fixture-012345678901234567890123456789")
	if err := writeInitialConfig(l, "", "auth.token", token); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(l.ConfigDir, "auth.token")
	before := tokenDescriptor(t, path)
	if err := nativeSecure(l, ""); err != nil {
		t.Fatal(err)
	}
	if got := tokenDescriptor(t, path); got != before {
		t.Fatal("initializer modified an already protected token")
	}
	data, err := secureconfig.ReadTokenFile(t.Context(), secureconfig.TokenFileOptions{Path: path, Root: l.ConfigDir, ReaderSID: sid, MaxBytes: 4096})
	if err != nil {
		t.Fatal("service token cannot be validated before registration", err)
	}
	defer clear(data)
	if string(data) != string(token) {
		t.Fatal("service token bytes changed")
	}
	for _, path := range []string{l.ConfigDir, filepath.Join(l.ConfigDir, "auth.token")} {
		sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		owner, _, err := sd.Owner()
		if err != nil || owner == nil || owner.String() != "S-1-5-32-544" {
			t.Fatal("new system configuration is not owned by Administrators", err)
		}
		control, _, err := sd.Control()
		if err != nil || control&windows.SE_DACL_PROTECTED == 0 {
			t.Fatal("system configuration inherited a DACL", err)
		}
	}
}

func TestWindowsSystemInitRefusesExistingCredentialOwnerWithoutChanges(t *testing.T) {
	for _, target := range []string{"token", "directory"} {
		t.Run(target, func(t *testing.T) {
			l, _ := windowsSystemConfigFixture(t)
			marker := []byte("existing-credential-marker-012345678901234567890123456789")
			path := filepath.Join(l.ConfigDir, "auth.token")
			if err := writeInitialConfig(l, "", "auth.token", marker); err != nil {
				t.Fatal(err)
			}
			principal, err := windows.GetCurrentProcessToken().GetTokenUser()
			if err != nil {
				t.Fatal(err)
			}
			changed := path
			if target == "directory" {
				changed = l.ConfigDir
			}
			if err := windows.SetNamedSecurityInfo(changed, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION, principal.User.Sid, nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			tokenBefore, directoryBefore := tokenDescriptor(t, path), tokenDescriptor(t, l.ConfigDir)
			err = nativeSecure(l, "")
			if err == nil {
				t.Fatal("existing individual-owned system credential was accepted")
			}
			if strings.Contains(err.Error(), string(marker)) || strings.Contains(err.Error(), path) {
				t.Fatal("credential or filesystem detail escaped safe diagnostic")
			}
			if got := tokenDescriptor(t, path); got != tokenBefore {
				t.Fatal("refusal changed existing token ownership or DACL")
			}
			if got := tokenDescriptor(t, l.ConfigDir); got != directoryBefore {
				t.Fatal("refusal changed existing directory ownership or DACL")
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil || string(after) != string(marker) {
				t.Fatal("refusal changed existing token contents", readErr)
			}
		})
	}
}
