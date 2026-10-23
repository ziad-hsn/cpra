package localadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestInitializationPortRejectsUnsafeRuntimeBeforeReading(t *testing.T) {
	for _, kind := range []string{"fifo", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			l := testLayout(t)
			if err := os.MkdirAll(l.ConfigDir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(l.ConfigDir, "runtime.yaml")
			var err error
			if kind == "fifo" {
				err = unix.Mkfifo(path, 0600)
			} else {
				err = os.Symlink(filepath.Join(t.TempDir(), "missing"), path)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := Init(l, "", 8060); err == nil {
				t.Fatal("unsafe runtime was read or accepted")
			}
		})
	}
}

func TestInstallUsesValidatedExecutableWhenSourceChanges(t *testing.T) {
	// The installer and filesystem are real; a private supervisor fixture
	// records commands without installing or controlling a host service.
	for _, update := range []bool{false, true} {
		name := "install"
		if update {
			name = "update"
		}
		t.Run(name, func(t *testing.T) {
			l := testLayout(t)
			if err := Init(l, "", 0); err != nil {
				t.Fatal(err)
			}
			fixture := t.TempDir()
			calls := filepath.Join(fixture, "supervisor-calls")
			writeServiceFixture(t, filepath.Join(fixture, "systemctl"), "#!/bin/sh\n"+
				"printf '%s\\n' \"$*\" >> \"$CPRA_TEST_SUPERVISOR_CALLS\"\n"+
				"if [ \"$2\" = show ]; then printf 'active\\n'; fi\n")
			t.Setenv("PATH", fixture)
			t.Setenv("CPRA_TEST_SUPERVISOR_CALLS", calls)
			candidate := filepath.Join(fixture, "candidate")
			validatedPath := filepath.Join(fixture, "validated-path")
			mutator := filepath.Join(fixture, "validation")
			writeServiceFixture(t, mutator, "#!/bin/sh\nset -eu\n"+
				"printf '%s\\n' \"$1\" > \"$CPRA_TEST_VALIDATED_PATH\"\n"+
				"printf 'not the validated executable\\n' > \"$CPRA_TEST_SOURCE\"\n")
			validated := []byte("#!/bin/sh\nexec /bin/sh \"$CPRA_TEST_VALIDATION\" \"$0\" \"$@\"\n")
			writeServiceFixture(t, candidate, string(validated))
			t.Setenv("CPRA_TEST_SOURCE", candidate)
			t.Setenv("CPRA_TEST_VALIDATION", mutator)
			t.Setenv("CPRA_TEST_VALIDATED_PATH", validatedPath)
			var authenticationKey string
			if update {
				if err := os.MkdirAll(l.BinDir, 0700); err != nil {
					t.Fatal(err)
				}
				writeServiceFixture(t, binaryPath(l), "existing managed executable\n")
				writeServiceFixture(t, l.ServiceFile, "existing service definition\n")
				hash, err := hashFile(binaryPath(l))
				if err != nil {
					t.Fatal(err)
				}
				record := Installation{Version: 1, Kind: "local", Binary: binaryPath(l), BinarySHA256: hash.SHA256, ServiceFile: l.ServiceFile}
				data, err := json.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(l.ConfigDir, "install.json"), data, 0600); err != nil {
					t.Fatal(err)
				}
				authenticationKey = backupTestKey(t, fixture)
			}
			if err := Install(context.Background(), l, candidate, "", update, authenticationKey); err != nil {
				t.Fatal(err)
			}
			installed, err := os.ReadFile(binaryPath(l))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(installed, validated) {
				t.Fatal("installed executable differs from the candidate that passed validation")
			}
			path, err := os.ReadFile(validatedPath)
			if err != nil {
				t.Fatal(err)
			}
			if filepath.Dir(filepath.Dir(strings.TrimSpace(string(path)))) != l.ConfigDir {
				t.Fatal("validation did not execute the installer-managed private copy")
			}
			entries, err := filepath.Glob(filepath.Join(l.ConfigDir, ".cpra-candidate-*"))
			if err != nil || len(entries) != 0 {
				t.Fatalf("candidate staging survived completed installation: %v %v", entries, err)
			}
			commands, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			want := "--user daemon-reload\n"
			if update {
				want = "--user show --property=ActiveState --value cpra.service\n--user stop cpra.service\n--user daemon-reload\n--user start cpra.service\n"
			}
			if string(commands) != want {
				t.Fatalf("supervisor sequence = %q, want %q", commands, want)
			}
		})
	}
}

func TestInstallValidationFailureRemovesStagedExecutable(t *testing.T) {
	l := testLayout(t)
	fixture := t.TempDir()
	candidate := filepath.Join(fixture, "candidate")
	writeServiceFixture(t, candidate, "#!/bin/sh\nexit 1\n")
	if err := Install(context.Background(), l, candidate, "", false, ""); err == nil || !strings.Contains(err.Error(), "candidate configuration validation") {
		t.Fatalf("invalid candidate was accepted: %v", err)
	}
	for _, path := range []string{binaryPath(l), l.ServiceFile, filepath.Join(l.ConfigDir, "install.json")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("failed validation published managed file %s: %v", path, err)
		}
	}
	entries, err := filepath.Glob(filepath.Join(l.ConfigDir, ".cpra-candidate-*"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("candidate staging survived validation failure: %v %v", entries, err)
	}
}

func writeServiceFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxUserInitRepairsPermissionsWithoutReplacingFiles(t *testing.T) {
	l := testLayout(t)
	if err := Init(l, "", 0); err != nil {
		t.Fatal(err)
	}
	// The token's configuration directory must already be trusted; initializer
	// repair cannot retroactively establish trust in a supplied credential.
	directories := []string{l.StateDir, l.LogDir, l.BinDir, filepath.Join(l.StateDir, "history")}
	for _, path := range directories {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0777); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string][]byte{
		filepath.Join(l.ConfigDir, "monitors.yaml"):        []byte("monitors: [] # operator edited\n"),
		filepath.Join(l.ConfigDir, "runtime.yaml"):         []byte("storage: {mode: raft, directory: " + l.StateDir + "}\n"),
		filepath.Join(l.StateDir, "history", "segment.db"): []byte("retained event fixture"),
		filepath.Join(l.LogDir, "cpra.log"):                []byte("retained diagnostic fixture"),
		binaryPath(l):                                      []byte("managed executable fixture"),
		l.ServiceFile:                                      []byte("managed service fixture"),
	}
	for path, data := range files {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0666); err != nil {
			t.Fatal(err)
		}
	}
	// Provider helpers are operator-owned configuration inputs, not starter
	// files. Initialization must not strip their executable permission.
	helper := filepath.Join(l.ConfigDir, "credential-helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := Init(l, "", 0); err != nil {
		t.Fatal(err)
	}
	for _, path := range directories {
		assertLinuxMode(t, path, 0700)
	}
	for path, want := range files {
		mode := os.FileMode(0600)
		if path == binaryPath(l) {
			mode = 0700
		}
		assertLinuxMode(t, path, mode)
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("initialization changed %s: %v", path, err)
		}
	}
	assertLinuxMode(t, helper, 0700)
}

func TestLinuxUserInitRejectsLinkedAndSpecialState(t *testing.T) {
	for _, kind := range []string{"symlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			l := testLayout(t)
			if err := Init(l, "", 0); err != nil {
				t.Fatal(err)
			}
			entry := filepath.Join(l.StateDir, "unexpected")
			target := filepath.Join(filepath.Dir(l.StateDir), "outside")
			if err := os.WriteFile(target, []byte("outside the installation"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(target, 0666); err != nil {
				t.Fatal(err)
			}
			var err error
			if kind == "symlink" {
				err = os.Symlink(target, entry)
			} else {
				err = unix.Mkfifo(entry, 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := Init(l, "", 0); err == nil {
				t.Fatal("initialization accepted non-regular state")
			}
			assertLinuxMode(t, target, 0666)
		})
	}
}

func TestLinuxUserInitRejectsLinkedExecutableAndServicePaths(t *testing.T) {
	for _, kind := range []string{"binary", "service", "service-parent"} {
		t.Run(kind, func(t *testing.T) {
			l := testLayout(t)
			if err := Init(l, "", 0); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(filepath.Dir(l.StateDir), "outside")
			if err := os.WriteFile(target, []byte("outside the installation"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(target, 0666); err != nil {
				t.Fatal(err)
			}
			entry := l.ServiceFile
			if kind == "binary" {
				if err := os.MkdirAll(l.BinDir, 0700); err != nil {
					t.Fatal(err)
				}
				entry = binaryPath(l)
			}
			if kind == "service-parent" {
				parent := filepath.Join(filepath.Dir(l.StateDir), "linked-service-dir")
				if err := os.Symlink(filepath.Dir(target), parent); err != nil {
					t.Fatal(err)
				}
				l.ServiceFile = filepath.Join(parent, filepath.Base(target))
			} else if err := os.Symlink(target, entry); err != nil {
				t.Fatal(err)
			}
			if err := Init(l, "", 0); err == nil {
				t.Fatal("initialization accepted a linked managed path")
			}
			assertLinuxMode(t, target, 0666)
		})
	}
}

func assertLinuxMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s permission = %04o, want %04o", path, got, want)
	}
}
