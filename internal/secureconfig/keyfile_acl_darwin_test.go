package secureconfig

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func darwinACLFixture(entries ...uint32) []byte {
	buffer := make([]byte, 12+44+24*len(entries))
	order := binary.NativeEndian
	order.PutUint32(buffer[:4], uint32(len(buffer)))
	order.PutUint32(buffer[4:8], 8)
	order.PutUint32(buffer[8:12], uint32(len(buffer)-12))
	order.PutUint32(buffer[12:16], 0x012cc16d)
	order.PutUint32(buffer[48:52], uint32(len(entries)))
	for index, flags := range entries {
		start := 56 + index*24
		buffer[start] = 1
		order.PutUint32(buffer[start+16:start+20], flags)
		order.PutUint32(buffer[start+20:start+24], 1)
	}
	return buffer
}

func TestDarwinACLMetadata(t *testing.T) {
	order := binary.NativeEndian
	absent := make([]byte, 12)
	order.PutUint32(absent[:4], 12)
	order.PutUint32(absent[4:8], 8)
	noACL := darwinACLFixture()
	order.PutUint32(noACL[48:52], ^uint32(0))
	noInheritance := darwinACLFixture(2)
	order.PutUint32(noInheritance[52:56], 1<<17)
	full := make([]uint32, 128)
	for index := range full {
		full[index] = 2
	}
	for name, buffer := range map[string][]byte{
		"absent": absent, "empty": darwinACLFixture(), "no ACL marker": noACL,
		"deny": darwinACLFixture(2), "inherited deny": darwinACLFixture(2 | 0x10),
		"inherit-only deny": darwinACLFixture(2 | 0x100 | 0x20 | 0x40),
		"no inheritance":    noInheritance,
		"maximum entries":   darwinACLFixture(full...),
	} {
		t.Run(name, func(t *testing.T) {
			if err := checkDarwinACL(buffer); err != nil {
				t.Fatal(err)
			}
		})
	}
	mutations := map[string]func([]byte) []byte{
		"missing header":      func(b []byte) []byte { return b[:3] },
		"truncated entries":   func(b []byte) []byte { return b[:len(b)-1] },
		"reported truncation": func(b []byte) []byte { order.PutUint32(b[:4], 4096); return b },
		"short record":        func(b []byte) []byte { order.PutUint32(b[:4], 8); return b },
		"negative offset":     func(b []byte) []byte { order.PutUint32(b[4:8], ^uint32(0)); return b },
		"overlapping offset":  func(b []byte) []byte { order.PutUint32(b[4:8], 0); return b },
		"overflowing length":  func(b []byte) []byte { order.PutUint32(b[8:12], ^uint32(0)); return b },
		"short security header": func(b []byte) []byte {
			order.PutUint32(b[:4], 16)
			order.PutUint32(b[8:12], 4)
			return b
		},
		"invalid magic":          func(b []byte) []byte { order.PutUint32(b[12:16], 0); return b },
		"missing entry":          func(b []byte) []byte { order.PutUint32(b[48:52], 2); return b },
		"excess entries":         func(b []byte) []byte { order.PutUint32(b[48:52], 129); return b },
		"no ACL with entry":      func(b []byte) []byte { order.PutUint32(b[48:52], ^uint32(0)); return b },
		"deferred inheritance":   func(b []byte) []byte { order.PutUint32(b[52:56], 1<<16); return b },
		"unrecognized ACL flags": func(b []byte) []byte { order.PutUint32(b[52:56], 1<<31); return b },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			if err := checkDarwinACL(mutate(darwinACLFixture(2))); !errors.Is(err, ErrKeyFile) {
				t.Fatalf("invalid metadata accepted: %v", err)
			}
		})
	}
	for name, flags := range map[string]uint32{
		"grant": 1, "inherit-only grant": 1 | 0x100 | 0x20,
		"audit": 3, "unknown deny flags": 2 | 1<<31,
	} {
		t.Run(name, func(t *testing.T) {
			if err := checkDarwinACL(darwinACLFixture(2, flags)); !errors.Is(err, ErrKeyFile) {
				t.Fatalf("extended authority accepted: %v", err)
			}
		})
	}
}

func TestDarwinACLVolumeMetadata(t *testing.T) {
	order := binary.NativeEndian
	fixture := func() []byte {
		buffer := make([]byte, 44)
		order.PutUint32(buffer[:4], 44)
		order.PutUint32(buffer[4:8], 0x400000)
		return buffer
	}
	if err := checkDarwinACLVolume(fixture()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func([]byte) []byte{
		"missing":             func(b []byte) []byte { return nil },
		"truncated":           func(b []byte) []byte { return b[:43] },
		"reported truncation": func(b []byte) []byte { order.PutUint32(b[:4], 48); return b },
		"short result":        func(b []byte) []byte { order.PutUint32(b[:4], 4); return b },
		"unsupported":         func(b []byte) []byte { order.PutUint32(b[4:8], 0); return b },
		"different attribute": func(b []byte) []byte { order.PutUint32(b[4:8], 1); return b },
		"native without valid": func(b []byte) []byte {
			order.PutUint32(b[4:8], 0)
			order.PutUint32(b[24:28], 0x400000)
			return b
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := checkDarwinACLVolume(mutate(fixture())); !errors.Is(err, ErrKeyFile) {
				t.Fatalf("unavailable filesystem permissions accepted: %v", err)
			}
		})
	}
}

func setDarwinTestACL(t *testing.T, path, rule string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "/bin/chmod", "+a", rule, path).CombinedOutput(); err != nil {
		t.Fatalf("set ACL on disposable test path: %v: %s", err, output)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if output, err := exec.CommandContext(ctx, "/bin/chmod", "-N", path).CombinedOutput(); err != nil {
			t.Errorf("remove ACL from disposable test path: %v: %s", err, output)
		}
	})
}

func TestKeyFileDarwinRejectsGrantedAccess(t *testing.T) {
	for _, scope := range []string{"key", "directory", "ancestor", "inherited"} {
		t.Run(scope, func(t *testing.T) {
			opts := testKeyFileOptions(t)
			if _, err := GenerateLocalKeyFile(t.Context(), opts); err != nil {
				t.Fatal(err)
			}
			path, rule := opts.Path, "everyone allow read"
			if scope == "directory" {
				path, rule = filepath.Dir(path), "everyone allow list,search"
			} else if scope == "ancestor" {
				path, rule = filepath.Dir(filepath.Dir(path)), "everyone allow add_file,add_subdirectory,delete_child"
			} else if scope == "inherited" {
				path, rule = filepath.Dir(path), "everyone allow read,file_inherit,only_inherit"
			}
			setDarwinTestACL(t, path, rule)
			if _, err := LoadLocalKeyFile(t.Context(), opts); !errors.Is(err, ErrKeyFile) {
				t.Fatalf("extended ACL grant accepted: %v", err)
			}
			if scope != "key" {
				opts.Path = filepath.Join(filepath.Dir(opts.Path), "next.key")
				if _, err := GenerateLocalKeyFile(t.Context(), opts); !errors.Is(err, ErrKeyFile) {
					t.Fatalf("generation accepted an extended directory grant: %v", err)
				}
				if _, err := os.Lstat(opts.Path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("rejected generation published a key: %v", err)
				}
			}
		})
	}
}

func TestKeyFileDarwinInheritsDenyOnlyACL(t *testing.T) {
	opts := testKeyFileOptions(t)
	if _, err := GenerateLocalKeyFile(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	setDarwinTestACL(t, filepath.Dir(opts.Path), "everyone deny execute,file_inherit,only_inherit")
	opts.Path = filepath.Join(filepath.Dir(opts.Path), "inherited.key")
	id, err := GenerateLocalKeyFile(t.Context(), opts)
	if err != nil {
		t.Fatalf("deny-only inheritance prevented key generation: %v", err)
	}
	loaded, err := LoadLocalKeyFile(t.Context(), opts)
	if err != nil || loaded.ID() != id {
		t.Fatalf("inherited key failed reload: %v", err)
	}
	file, err := os.Open(opts.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	attributes := unix.Attrlist{Bitmapcount: unix.ATTR_BIT_MAP_COUNT, Commonattr: unix.ATTR_CMN_EXTENDED_SECURITY}
	var buffer [3128]byte
	if err := readDarwinAttributes(file, "", attributes, buffer[:]); err != nil {
		t.Fatal(err)
	}
	if binary.NativeEndian.Uint32(buffer[8:12]) < 68 || binary.NativeEndian.Uint32(buffer[72:76])&0x1f != 0x12 {
		t.Fatal("the fixture did not create an inherited deny entry")
	}
}

func TestKeyFileDarwinAllowsDenyOnlyACL(t *testing.T) {
	opts := testKeyFileOptions(t)
	id, err := GenerateLocalKeyFile(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	setDarwinTestACL(t, filepath.Dir(opts.Path), "everyone deny delete")
	setDarwinTestACL(t, opts.Path, "everyone deny delete")
	loaded, err := LoadLocalKeyFile(t.Context(), opts)
	if err != nil || loaded.ID() != id {
		t.Fatalf("deny-only ACL prevented loading the original key: %v", err)
	}
	opts.Path = filepath.Join(filepath.Dir(opts.Path), "next.key")
	if _, err := GenerateLocalKeyFile(t.Context(), opts); err != nil {
		t.Fatalf("deny-only parent ACL prevented key generation: %v", err)
	}
}

func TestKeyFileDarwinACLUsesOpenedFile(t *testing.T) {
	opts := testKeyFileOptions(t)
	if _, err := GenerateLocalKeyFile(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(opts.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	old := opts.Path + ".old"
	if err := os.Rename(opts.Path, old); err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateLocalKeyFile(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	setDarwinTestACL(t, old, "everyone allow read")
	if err := checkKeyAccessACL(file, ""); !errors.Is(err, ErrKeyFile) {
		t.Fatalf("opened key ACL was replaced by current-path metadata: %v", err)
	}
	if err := checkKeyAccessACL(nil, opts.Path); err != nil {
		t.Fatalf("new private file inherited the old file's ACL: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := checkKeyAccessACL(file, ""); !errors.Is(err, ErrKeyFile) {
		t.Fatalf("closed-handle metadata error accepted: %v", err)
	}
	if err := checkKeyAccessACL(nil, opts.Path+".missing"); !errors.Is(err, ErrKeyFile) {
		t.Fatalf("missing-path metadata error accepted: %v", err)
	}
}

func FuzzDarwinACLMetadata(f *testing.F) {
	f.Add(darwinACLFixture(2))
	f.Add(darwinACLFixture(1))
	f.Add([]byte{0})
	f.Fuzz(func(t *testing.T, buffer []byte) {
		if err := checkDarwinACL(buffer); err != nil && !errors.Is(err, ErrKeyFile) {
			t.Fatalf("unexpected parser error: %v", err)
		}
		if err := checkDarwinACLVolume(buffer); err != nil && !errors.Is(err, ErrKeyFile) {
			t.Fatalf("unexpected volume parser error: %v", err)
		}
	})
}
