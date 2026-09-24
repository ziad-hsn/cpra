package management

import (
	"bufio"
	"bytes"
	"context"
	"crypto/cipher"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

func reselectionRootOptions() reselectionSpoolRootOptions {
	return reselectionSpoolRootOptions{MaxSpools: 4, MaxCleanupEntries: 16, MaxCleanupBytes: 8 << 20}
}

func reselectionTestRoot(t *testing.T) (*reselectionSpoolRoot, string) {
	t.Helper()
	parent := t.TempDir()
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := protectNewStartupDirectory(parent); err != nil {
		t.Fatal(err)
	}
	r, err := newReselectionSpoolRoot(t.Context(), parent, reselectionRootOptions())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	return r, parent
}

func reselectionTestSpool(t *testing.T, root *reselectionSpoolRoot) *reselectionSpool {
	t.Helper()
	s, err := newReselectionSpool(t.Context(), root, reselectionSpoolOptions{AttemptID: uuid.NewString(), MaxPlaintextBytes: 4 << 20, MaxEncodedBytes: 8 << 20, MaxRecords: 256})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestReselectionSpoolEncryptedFramesAndBorrowedBuffers(t *testing.T) {
	r, _ := reselectionTestRoot(t)
	s := reselectionTestSpool(t, r)
	secret := []byte("credential-and-private-comment-must-not-enter-files")
	source, err := s.appendSource(t.Context(), 1, 0, secret)
	if err != nil {
		t.Fatal(err)
	}
	suffix, err := s.appendSuffix(t.Context(), 7, []byte(`{"spec":{"value":"credential-and-private-comment-must-not-enter-files"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var borrowed []byte
	if err := s.withRecord(t.Context(), source, reselectionSource, func(raw []byte) error {
		if !bytes.Equal(raw, secret) {
			t.Fatal("source changed")
		}
		borrowed = raw
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(borrowed, make([]byte, len(borrowed))) {
		t.Fatal("borrowed plaintext survived callback")
	}
	if err := s.withRecord(t.Context(), suffix, reselectionSuffix, func(raw []byte) error {
		if !bytes.Contains(raw, secret) {
			t.Fatal("suffix changed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(s.directory, reselectionRecordsFile))
	if err != nil || bytes.Contains(raw, secret) || bytes.Contains(raw, s.key[:]) {
		t.Fatal("private input/key persisted", err)
	}
	counts, err := s.accounting()
	if err != nil || counts.Records != 2 || counts.EncodedBytes != int64(len(raw)) || counts.PlaintextBytes != int64(source.length+suffix.length) {
		t.Fatal("incorrect exact frame accounting", counts, err)
	}
	for _, v := range []any{s, *s, r, *r} {
		text := fmt.Sprintf("%v %#v %+v", v, v, v)
		if strings.Contains(text, s.directory) || strings.Contains(text, string(secret)) || strings.Contains(text, s.options.AttemptID) {
			t.Fatal("formatter disclosed private state")
		}
		if _, err := json.Marshal(v); err == nil {
			t.Fatal("private spool serialized")
		}
	}
	directory := s.directory
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s.key != [32]byte{} || s.aead != nil {
		t.Fatal("owned key/cipher not cleared")
	}
	if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("closed attempt retained files", err)
	}
}

func TestReselectionSpoolReferencesAndCorruption(t *testing.T) {
	for _, fault := range []string{"purpose", "offset", "length", "coordinate", "ciphertext", "header", "truncated", "different-key", "attempt-identity", "swapped-frame"} {
		t.Run(fault, func(t *testing.T) {
			r, _ := reselectionTestRoot(t)
			s := reselectionTestSpool(t, r)
			ref, err := s.appendSource(t.Context(), 1, 0, []byte("exact-original-frame"))
			if err != nil {
				t.Fatal(err)
			}
			purpose := reselectionSource
			corrupt := false
			switch fault {
			case "purpose":
				purpose = reselectionSuffix
			case "offset":
				ref.offset++
			case "length":
				ref.length++
			case "coordinate":
				ref.position++
			case "ciphertext", "header":
				offset := int64(reselectionFrameHeader + 5)
				if fault == "header" {
					offset = 8
				}
				var b [1]byte
				if _, err := s.file.ReadAt(b[:], offset); err != nil {
					t.Fatal(err)
				}
				b[0] ^= 1
				if _, err := s.file.WriteAt(b[:], offset); err != nil {
					t.Fatal(err)
				}
				corrupt = true
			case "truncated":
				if err := s.file.Truncate(s.counts.EncodedBytes - 1); err != nil {
					t.Fatal(err)
				}
				corrupt = true
			case "different-key":
				other := reselectionTestSpool(t, r)
				s.aead = other.aead
				corrupt = true
			case "attempt-identity":
				s.options.AttemptID = uuid.NewString()
				corrupt = true
			case "swapped-frame":
				other := reselectionTestSpool(t, r)
				if _, err := other.appendSource(t.Context(), 1, 0, []byte("exact-original-frame")); err != nil {
					t.Fatal(err)
				}
				frame, err := os.ReadFile(filepath.Join(other.directory, reselectionRecordsFile))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.file.WriteAt(frame, 0); err != nil {
					t.Fatal(err)
				}
				corrupt = true
			}
			if err := s.withRecord(t.Context(), ref, purpose, func([]byte) error { t.Fatal("invalid record reached callback"); return nil }); err == nil {
				t.Fatal("invalid record accepted")
			}
			if corrupt {
				if _, err := s.appendSuffix(t.Context(), 1, []byte("new")); !errors.Is(err, errReselectionSpool) {
					t.Fatal("corrupt spool resumed", err)
				}
			} else if s.failed {
				t.Fatal("bad caller reference poisoned intact ciphertext")
			}
		})
	}
}

func TestReselectionSpoolClearsBorrowedBufferOnPanic(t *testing.T) {
	r, _ := reselectionTestRoot(t)
	s := reselectionTestSpool(t, r)
	ref, err := s.appendSource(t.Context(), 1, 0, []byte("borrowed-private-frame"))
	if err != nil {
		t.Fatal(err)
	}
	var borrowed []byte
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("fixture callback did not panic")
			}
		}()
		_ = s.withRecord(t.Context(), ref, reselectionSource, func(raw []byte) error { borrowed = raw; panic("fixture") })
	}()
	if len(borrowed) == 0 || !bytes.Equal(borrowed, make([]byte, len(borrowed))) {
		t.Fatal("panic retained borrowed plaintext")
	}
	if err := s.withRecord(t.Context(), ref, reselectionSource, func([]byte) error { return nil }); err != nil {
		t.Fatal("panic leaked mutex", err)
	}
}

func TestReselectionSpoolConcurrentAppendAndRootClose(t *testing.T) {
	r, parent := reselectionTestRoot(t)
	s := reselectionTestSpool(t, r)
	var workers sync.WaitGroup
	for n := 0; n < 12; n++ {
		workers.Add(1)
		go func(n int) {
			defer workers.Done()
			ref, err := s.appendSuffix(t.Context(), uint64(n+1), []byte("concurrent"))
			if err != nil {
				t.Error(err)
				return
			}
			if err := s.withRecord(t.Context(), ref, reselectionSuffix, func(raw []byte) error {
				if string(raw) != "concurrent" {
					t.Error("changed record")
				}
				return nil
			}); err != nil {
				t.Error(err)
			}
		}(n)
	}
	workers.Wait()
	entered, release, readDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		readDone <- s.withRecord(t.Context(), s.records[0], reselectionSuffix, func([]byte) error { close(entered); <-release; return nil })
	}()
	<-entered
	closed := make(chan error, 2)
	go func() { closed <- r.Close() }()
	deadline := time.Now().Add(time.Second)
	for !r.closed.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !r.closed.Load() {
		close(release)
		t.Fatal("root close did not stop admission")
	}
	go func() { closed <- r.Close() }()
	if second, err := newReselectionSpoolRoot(t.Context(), parent, reselectionRootOptions()); !errors.Is(err, errReselectionSpoolLocked) {
		if second != nil {
			_ = second.Close()
		}
		close(release)
		t.Fatal("ownership released before callback ended", err)
	}
	select {
	case <-closed:
		close(release)
		t.Fatal("Close returned while borrower retained plaintext")
	default:
	}
	close(release)
	if err := <-readDone; err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 2; n++ {
		if err := <-closed; err != nil {
			t.Fatal(err)
		}
	}
	if !s.closed || s.key != [32]byte{} {
		t.Fatal("root close left active spool")
	}
}

func TestReselectionSpoolQuotasPrecedeWrites(t *testing.T) {
	for _, quota := range []string{"plaintext", "encoded", "records"} {
		t.Run(quota, func(t *testing.T) {
			r, _ := reselectionTestRoot(t)
			s := reselectionTestSpool(t, r)
			if _, err := s.appendSource(t.Context(), 1, 0, []byte("one")); err != nil {
				t.Fatal(err)
			}
			before, _ := s.accounting()
			switch quota {
			case "plaintext":
				s.options.MaxPlaintextBytes = before.PlaintextBytes
			case "encoded":
				s.options.MaxEncodedBytes = before.EncodedBytes + reselectionFrameHeader + 28
			case "records":
				s.options.MaxRecords = before.Records
			}
			if _, err := s.appendSuffix(t.Context(), 1, []byte("two")); !errors.Is(err, errReselectionSpoolQuota) {
				t.Fatal("quota not enforced", err)
			}
			after, err := s.accounting()
			if err != nil || before != after {
				t.Fatal("quota rejection changed file/prefix", err)
			}
		})
	}
	r, _ := reselectionTestRoot(t)
	for _, count := range []int{0, reselectionRecordLimit + 1} {
		if _, err := newReselectionSpool(t.Context(), r, reselectionSpoolOptions{AttemptID: uuid.NewString(), MaxRecords: count, MaxPlaintextBytes: 1024, MaxEncodedBytes: 4096}); err == nil {
			t.Fatal("record index unbounded")
		}
	}
	s := reselectionTestSpool(t, r)
	if _, err := s.appendSuffix(t.Context(), 1, make([]byte, reselectionFrameLimit+1)); !errors.Is(err, ErrValidation) {
		t.Fatal("oversized frame accepted", err)
	}
}

func TestReselectionSpoolFailedWritesPoisonWithoutTruncation(t *testing.T) {
	for _, mode := range []string{"partial-enospc", "short-write", "sync-enospc", "canceled-after-write"} {
		t.Run(mode, func(t *testing.T) {
			r, _ := reselectionTestRoot(t)
			s := reselectionTestSpool(t, r)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch mode {
			case "partial-enospc", "short-write":
				s.writeAt = func(data []byte, offset int64) (int, error) {
					n, err := s.file.WriteAt(data[:7], offset)
					if err != nil {
						return n, err
					}
					if mode == "partial-enospc" {
						return n, syscall.ENOSPC
					}
					return n, nil
				}
			case "sync-enospc":
				s.syncFile = func() error { return syscall.ENOSPC }
			case "canceled-after-write":
				s.syncFile = func() error { cancel(); return nil }
			}
			if _, err := s.appendSource(ctx, 1, 0, []byte("private")); err == nil {
				t.Fatal("failed append reported success")
			}
			info, err := s.file.Stat()
			if err != nil || info.Size() == 0 {
				t.Fatal("test did not produce partial/uncertain bytes", err)
			}
			if s.counts != (reselectionSpoolAccounting{}) {
				t.Fatal("failed write reported accepted frame")
			}
			if _, err := s.appendSuffix(t.Context(), 1, []byte("again")); !errors.Is(err, errReselectionSpool) {
				t.Fatal("failed spool admitted new work", err)
			}
			next, _ := s.file.Stat()
			if next.Size() != info.Size() {
				t.Fatal("failed spool silently truncated or rewrote bytes")
			}
		})
	}
}

type reselectionCancelAfterSeal struct {
	cipher.AEAD
	cancel context.CancelFunc
}

func (c reselectionCancelAfterSeal) Seal(dst, nonce, plaintext, additionalData []byte) []byte {
	sealed := c.AEAD.Seal(dst, nonce, plaintext, additionalData)
	c.cancel()
	return sealed
}

func TestReselectionSpoolCancellationAfterSealRetiresKey(t *testing.T) {
	r, _ := reselectionTestRoot(t)
	s := reselectionTestSpool(t, r)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s.aead = reselectionCancelAfterSeal{AEAD: s.aead, cancel: cancel}
	if _, err := s.appendSource(ctx, 1, 0, []byte("private")); !errors.Is(err, context.Canceled) {
		t.Fatal("sealed canceled frame accepted", err)
	}
	if info, err := s.file.Stat(); err != nil || info.Size() != 0 {
		t.Fatal("canceled frame entered file", err)
	}
	if _, err := s.appendSource(t.Context(), 1, 0, []byte("again")); !errors.Is(err, errReselectionSpool) {
		t.Fatal("canceled seal allowed further encryption with same key", err)
	}
}

func TestReselectionSpoolRootExclusiveAndCleanupGuards(t *testing.T) {
	r, parent := reselectionTestRoot(t)
	if second, err := newReselectionSpoolRoot(t.Context(), parent, reselectionRootOptions()); !errors.Is(err, errReselectionSpoolLocked) {
		if second != nil {
			_ = second.Close()
		}
		t.Fatal("two root owners overlapped", err)
	}
	s := reselectionTestSpool(t, r)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if !s.closed || s.key != [32]byte{} {
		t.Fatal("root released lock with live attempt")
	}
	unknown := filepath.Join(r.directory, "unrelated-data")
	if err := os.WriteFile(unknown, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if reopened, err := newReselectionSpoolRoot(t.Context(), parent, reselectionRootOptions()); err == nil {
		_ = reopened.Close()
		t.Fatal("unknown root entry silently removed")
	}
	if raw, err := os.ReadFile(unknown); err != nil || string(raw) != "keep" {
		t.Fatal("cleanup damaged unrelated data", err)
	}
}

func TestReselectionSpoolClosePreservesReplacements(t *testing.T) {
	for _, replacement := range []string{"file", "directory", "symlink"} {
		t.Run(replacement, func(t *testing.T) {
			r, _ := reselectionTestRoot(t)
			s := reselectionTestSpool(t, r)
			if _, err := s.appendSource(t.Context(), 1, 0, []byte("owned")); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(s.directory, reselectionRecordsFile)
			if replacement == "file" {
				if err := os.Rename(path, path+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Rename(s.directory, s.directory+".original"); err != nil {
					t.Fatal(err)
				}
				if replacement == "symlink" {
					other := t.TempDir()
					if err := os.WriteFile(filepath.Join(other, reselectionRecordsFile), []byte("replacement"), 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(other, s.directory); err != nil {
						t.Skip("native symlink permission unavailable")
					}
				} else {
					if err := os.Mkdir(s.directory, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := s.Close(); !errors.Is(err, errReselectionSpool) {
				t.Fatal("replacement accepted as owned cleanup", err)
			}
			if s.key != [32]byte{} || s.aead != nil || s.file != nil {
				t.Fatal("cleanup failure retained private key/handle")
			}
			if data, err := os.ReadFile(path); err != nil || string(data) != "replacement" {
				t.Fatal("cleanup removed replacement", err)
			}
			if next, err := newReselectionSpool(t.Context(), r, s.options); !errors.Is(err, errReselectionSpool) {
				if next != nil {
					_ = next.Close()
				}
				t.Fatal("cleanup failure silently freed admission capacity", err)
			}
		})
	}
}

func TestReselectionSpoolUnmarkedOwnershipRefusedWithoutRepair(t *testing.T) {
	r, parent := reselectionTestRoot(t)
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(r.directory, reselectionOwnershipFile)
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		if err := tx.DeleteBucket([]byte("owner")); err != nil {
			return err
		}
		b, err := tx.CreateBucket([]byte("unknown-owner"))
		if err != nil {
			return err
		}
		return b.Put([]byte("keep"), []byte("do not repair unknown state"))
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if opened, err := newReselectionSpoolRoot(t.Context(), parent, reselectionRootOptions()); !errors.Is(err, errReselectionSpool) {
		if opened != nil {
			_ = opened.Close()
		}
		t.Fatal("unmarked ownership database was silently adopted", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("unrecognized ownership database was changed", err)
	}
}

func TestReselectionSpoolCleanupCarriesCapturedIdentity(t *testing.T) {
	r, _ := reselectionTestRoot(t)
	s := reselectionTestSpool(t, r)
	if _, err := s.appendSource(t.Context(), 1, 0, []byte("owned")); err != nil {
		t.Fatal(err)
	}
	captured := reselectionSpoolFiles{directory: s.directory, directoryInfo: s.directoryInfo, fileInfo: s.fileInfo}
	path := filepath.Join(s.directory, reselectionRecordsFile)
	if err := os.Rename(path, path+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := captured.remove(); !errors.Is(err, errReselectionSpool) {
		t.Fatal("stale identity deleted replacement", err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "replacement" {
		t.Fatal("replacement removed", err)
	}
	_ = s.Close() // Close must also leave it in place, while releasing its key.
}

func TestReselectionSpoolCrashChild(t *testing.T) {
	parent := os.Getenv("CPRA_RESELECTION_SPOOL_CHILD")
	if parent == "" {
		t.Skip("subprocess fixture only")
	}
	r, err := newReselectionSpoolRoot(context.Background(), parent, reselectionRootOptions())
	if err != nil {
		t.Fatal(err)
	}
	s, err := newReselectionSpool(context.Background(), r, reselectionSpoolOptions{AttemptID: uuid.NewString(), MaxRecords: 3, MaxPlaintextBytes: 4096, MaxEncodedBytes: 8192})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.appendSource(context.Background(), 1, 0, []byte("kill-test-private-source-comment")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.appendSuffix(context.Background(), 1, []byte("kill-test-private-provider-secret")); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(os.Stdout, "RESELECTION_SPOOL_READY"); err != nil {
		t.Fatal(err)
	}
	select {}
}

func TestReselectionSpoolKilledProcessLeavesOnlyDisposableCiphertext(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	if err := protectNewStartupDirectory(parent); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReselectionSpoolCrashChild$", "-test.timeout=15s")
	cmd.Env = append(os.Environ(), "CPRA_RESELECTION_SPOOL_CHILD="+parent)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	scanner := bufio.NewScanner(stdout)
	ready := false
	for scanner.Scan() {
		if scanner.Text() == "RESELECTION_SPOOL_READY" {
			ready = true
			break
		}
	}
	if !ready {
		t.Fatal("child did not reach encrypted staging", scanner.Err(), stderr.String())
	}
	if second, err := newReselectionSpoolRoot(t.Context(), parent, reselectionRootOptions()); !errors.Is(err, errReselectionSpoolLocked) {
		if second != nil {
			_ = second.Close()
		}
		t.Fatal("live child ownership bypassed", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("child was not forcibly terminated")
	}
	waited = true
	directory := filepath.Join(parent, reselectionRootDirectory)
	files, err := filepath.Glob(filepath.Join(directory, "attempt-*", reselectionRecordsFile))
	if err != nil || len(files) != 1 {
		t.Fatal("missing crash leftover", err)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil || len(raw) == 0 || bytes.Contains(raw, []byte("kill-test-private")) {
		t.Fatal("crash left plaintext or no staged evidence", err)
	}
	limited := reselectionRootOptions()
	limited.MaxCleanupBytes = 1
	if opened, err := newReselectionSpoolRoot(t.Context(), parent, limited); !errors.Is(err, errReselectionSpoolQuota) {
		if opened != nil {
			_ = opened.Close()
		}
		t.Fatal("cleanup byte quota bypassed", err)
	}
	if _, err := os.Stat(files[0]); err != nil {
		t.Fatal("failed cleanup removed ciphertext", err)
	}
	r, err := newReselectionSpoolRoot(t.Context(), parent, reselectionRootOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.cleanupEntries != 1 || r.cleanupFileBytes != int64(len(raw)) {
		t.Fatal("cleanup did not account exact stale ciphertext")
	}
	if _, err := os.Stat(files[0]); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale ciphertext retained after exclusive cleanup", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].Name() != reselectionOwnershipFile {
		t.Fatal("unexpected leftover/key files", err)
	}
}
