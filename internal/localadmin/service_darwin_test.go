package localadmin

import (
	"bytes"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDarwinInitPreservesCredentialHelpersAndRepairsManagedModes(t *testing.T) {
	l := testLayout(t)
	if err := Init(l, ""); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(l.ConfigDir, "credential-helper")
	content := []byte("#!/bin/sh\nexit 0\n")
	if err := os.WriteFile(helper, content, 0700); err != nil {
		t.Fatal(err)
	}
	managed := filepath.Join(l.ConfigDir, "runtime.yaml")
	before, err := os.ReadFile(managed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(managed, 0666); err != nil {
		t.Fatal(err)
	}
	if err := Init(l, ""); err != nil {
		t.Fatal(err)
	}
	for path, mode := range map[string]os.FileMode{helper: 0700, managed: 0600} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("permission repair changed %s: %v %v", path, info, err)
		}
	}
	for path, want := range map[string][]byte{helper: content, managed: before} {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("permission repair changed contents of %s: %v", path, err)
		}
	}
}

func TestDarwinInitRejectsLinkedAndSpecialState(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			l := testLayout(t)
			if err := Init(l, ""); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(filepath.Dir(l.StateDir), "outside")
			if err := os.WriteFile(outside, []byte("outside managed state"), 0666); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(outside, 0666); err != nil {
				t.Fatal(err)
			}
			entry := filepath.Join(l.StateDir, "unexpected")
			var err error
			if kind == "symlink" {
				err = os.Symlink(outside, entry)
			} else {
				err = unix.Mkfifo(entry, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := Init(l, ""); err == nil {
				t.Fatal("initialization accepted non-regular state")
			}
			info, err := os.Lstat(outside)
			if err != nil || info.Mode().Perm() != 0666 {
				t.Fatalf("rejected entry changed an outside file: %v %v", info, err)
			}
		})
	}
}

func TestLaunchdStatusParser(t *testing.T) {
	s := parseLaunchdStatus("gui/501/io.github.ziad-hsn.cpra = {\n state = running\n pid = 1234\n}\n")
	if !s.loaded || !s.running || s.pid != 1234 {
		t.Fatal(s)
	}
	s = parseLaunchdStatus("state = waiting\nlast exit code = 1")
	if !s.loaded || s.running || s.pid != 0 {
		t.Fatal(s)
	}
}
func TestLaunchdArgumentsAreXMLData(t *testing.T) {
	l := testLayout(t)
	l.ConfigDir += " with <&> characters"
	value, err := Render(l, "")
	if err != nil {
		t.Fatal(err)
	}
	decoder := xml.NewDecoder(strings.NewReader(value))
	found := false
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if text, ok := token.(xml.CharData); ok && strings.Contains(string(text), "with <&> characters") {
			found = true
		}
	}
	if !found {
		t.Fatal("plist escaped arguments did not round trip")
	}
	if strings.Contains(value, "<key>UserName</key>") {
		t.Fatal("LaunchAgent changed identity")
	}
	l.Scope = "system"
	value, err = Render(l, "_cpra")
	if err != nil || !strings.Contains(value, "<key>UserName</key><string>_cpra</string>") {
		t.Fatalf("missing explicit daemon identity: %s %v", value, err)
	}
}
