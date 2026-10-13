package persistence

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func collectionGCTestPrivate(t *testing.T, path string) {
	t.Helper()
	collectionGCTestACL(t, path, "")
}

func collectionGCTestWide(t *testing.T, path string, _ bool) {
	t.Helper()
	collectionGCTestACL(t, path, "(A;OICI;FA;;;WD)")
}

func collectionGCTestACL(t *testing.T, path, extra string) {
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
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		t.Fatal(err)
	}
}

func TestCollectionGCWindowsRootAnchorPreventsReplacement(t *testing.T) {
	f := newCollectionGCFixture(t)
	r := f.open(t)
	if err := os.Rename(f.dir, f.dir+"-moved"); err == nil {
		t.Fatal("open Windows root did not fence path replacement")
	}
	if step := gcTestStep(t, r, f.old); step.Phase != "intent" {
		t.Fatal("failed rename invalidated original root", step)
	}
}

func TestCollectionGCWindowsPartialTemporaryIntentBlocksAdmission(t *testing.T) {
	f := newCollectionGCFixture(t)
	r := f.open(t)
	if err := os.WriteFile(filepath.Join(f.dir, collectionGCIntentTemporary), []byte("incomplete"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := gcTestAttempt(t, r, f.old); !errors.Is(err, errCollectionGCBlocked) {
		t.Fatal("temporary intent replaced or ignored", err)
	}
	if _, err := os.Stat(filepath.Join(f.dir, f.old, "ledger.db")); err != nil {
		t.Fatal("uncommitted intent authorized deletion", err)
	}
	f.assertProtected(t)
}

func TestCollectionGCWindowsRemoveRejectsReplacementHandle(t *testing.T) {
	f := newCollectionGCFixture(t)
	r := f.open(t)
	entry, err := r.openCandidate(f.old, false)
	if err != nil {
		t.Fatal(err)
	}
	defer entry.close()
	path := filepath.Join(f.dir, f.old, "ledger.db")
	if err := os.Rename(path, filepath.Join(f.dir, "retained-old.db")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := collectionGCRemove(entry.root, entry.dir, "ledger.db", entry.database, false); !errors.Is(err, errCollectionGCBlocked) {
		t.Fatal("new object deleted using old identity", err)
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "replacement" {
		t.Fatal("replacement file changed", err)
	}
}
