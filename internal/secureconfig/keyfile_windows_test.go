package secureconfig

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func keyFileTestDirectory(t *testing.T) string {
	t.Helper()
	parent := os.Getenv("CPRA_KEY_TEST_PARENT")
	if parent == "" {
		return t.TempDir()
	}
	if !filepath.IsAbs(parent) {
		t.Fatal("CPRA_KEY_TEST_PARENT must be an absolute Windows path")
	}
	path := filepath.Join(parent, "cpra-key-test-"+rand.Text())
	sd, err := newKeySecurityDescriptor(KeyFileOptions{}, true)
	if err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.CreateDirectory(name, &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(path); err != nil {
			t.Error(err)
		}
	})
	return path
}

func TestKeyFileWindowsAncestorProtection(t *testing.T) {
	opts := testKeyFileOptions(t)
	for path := filepath.Dir(filepath.Dir(opts.Path)); ; path = filepath.Dir(path) {
		sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		if err := checkKeySecurityDescriptor(sd, opts, false); err != nil {
			t.Fatalf("ancestor %q: %v; protection=%s", path, err, sd.String())
		}
		if filepath.Dir(path) == path {
			break
		}
	}
}

func TestKeyFileWindowsRejectsUnsafeDACL(t *testing.T) {
	opts := testKeyFileOptions(t)
	if _, err := GenerateLocalKeyFile(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	owner, err := keyUserSID()
	if err != nil {
		t.Fatal(err)
	}
	for _, sddl := range []string{
		"O:" + owner + "D:P(A;;FA;;;" + owner + ")(A;;FR;;;WD)",
		"O:" + owner + "D:(A;;FA;;;" + owner + ")",
	} {
		sd, err := windows.SecurityDescriptorFromString(sddl)
		if err != nil {
			t.Fatal(err)
		}
		if err := checkKeySecurityDescriptor(sd, opts, true); !errors.Is(err, ErrKeyFile) {
			t.Fatalf("unsafe DACL accepted: %v", err)
		}
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + owner + ")(A;;FR;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(opts.Path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLocalKeyFile(context.Background(), opts); !errors.Is(err, ErrKeyFile) {
		t.Fatalf("world-readable file accepted: %v", err)
	}
}

func TestKeyFileWindowsServiceReaderPolicy(t *testing.T) {
	opts := testKeyFileOptions(t)
	opts.ReaderSID = "S-1-5-80-123-456-789-101-112"
	sd, err := newKeySecurityDescriptor(opts, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkKeySecurityDescriptor(sd, opts, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sd.String(), ";;;WD") || strings.Contains(sd.String(), ";;;BU") {
		t.Fatal("broad reader principal")
	}
	for _, rights := range []string{"FA", "FW", "WD", "WO", "DC"} {
		sd, err := windows.SecurityDescriptorFromString("O:BAD:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;" + rights + ";;;" + opts.ReaderSID + ")")
		if err != nil {
			t.Fatal(err)
		}
		if err := checkKeySecurityDescriptor(sd, opts, true); !errors.Is(err, ErrKeyFile) {
			t.Fatalf("service mutation right %s accepted: %v", rights, err)
		}
	}
	opts.ReaderSID = "S-1-1-0"
	if _, err := GenerateLocalKeyFile(context.Background(), opts); !errors.Is(err, ErrKeyFile) {
		t.Fatal("Everyone accepted as service reader")
	}
	opts.ReaderSID = ""
	group := 10
	opts.ReaderGroupID = &group
	if _, err := GenerateLocalKeyFile(context.Background(), opts); !errors.Is(err, ErrKeyFile) {
		t.Fatal("Unix access option ignored")
	}
}

func TestKeyFileWindowsProtectedOwnerAndLinks(t *testing.T) {
	opts := testKeyFileOptions(t)
	if _, err := GenerateLocalKeyFile(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(opts.Path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkKeySecurityDescriptor(sd, opts, true); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(opts.Path, opts.Path+".alias"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLocalKeyFile(context.Background(), opts); !errors.Is(err, ErrKeyFile) {
		t.Fatalf("hard-linked key accepted: %v", err)
	}
}

func TestKeyFileWindowsResolvedAliases(t *testing.T) {
	opts := testKeyFileOptions(t)
	id, err := GenerateLocalKeyFile(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	alias := opts
	alias.Path += ".alias"
	if err := os.Symlink(opts.Path, alias.Path); err != nil {
		if errors.Is(err, windows.ERROR_PRIVILEGE_NOT_HELD) {
			t.Skip("Windows symlink creation requires Developer Mode or the symbolic-link privilege")
		}
		t.Fatal(err)
	}
	loaded, err := LoadLocalKeyFile(context.Background(), alias)
	if err != nil || loaded.ID() != id {
		t.Fatalf("safe external alias: %v", err)
	}
	if _, err := GenerateLocalKeyFile(context.Background(), alias); !errors.Is(err, ErrKeyFile) {
		t.Fatalf("generation followed alias: %v", err)
	}
	if err := os.Mkdir(opts.DataDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(opts.DataDirectory, "misplaced.key")
	if err := os.Rename(opts.Path, inside); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(alias.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inside, alias.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadLocalKeyFile(context.Background(), alias); !errors.Is(err, ErrKeyFile) {
		t.Fatalf("state alias accepted: %v", err)
	}
}
