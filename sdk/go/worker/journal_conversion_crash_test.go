//go:build externaljobs

package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Pause after the production copy writes a nonempty partial backup of the
// encrypted journal. This boundary may contain only bbolt metadata pages.
// The parent kills this process; no cleanup runs.
type conversionPauseContext struct {
	context.Context
	directory, marker string
}

func (c conversionPauseContext) Err() error {
	files, err := filepath.Glob(filepath.Join(c.directory, "worker.format2-*.db.partial-*"))
	if err == nil && len(files) == 1 {
		if info, err := os.Stat(files[0]); err == nil && info.Size() > 0 {
			if err := os.WriteFile(c.marker, []byte("partial-written\n"), 0600); err != nil {
				return err
			}
			<-c.Context.Done()
		}
	}
	return c.Context.Err()
}

func TestJournalConversionProcess(t *testing.T) {
	if os.Getenv("CPRA_TEST_JOURNAL_CONVERSION") != "1" {
		return
	}
	c := Config{ServerID: "server-epoch-1", WorkerID: "worker-1", WorkerUID: "worker-uid-1", StateDir: os.Getenv("CPRA_TEST_JOURNAL_STATE"), WrappingKeyPath: os.Getenv("CPRA_TEST_JOURNAL_KEY")}
	var ctx context.Context = context.Background()
	if marker := os.Getenv("CPRA_TEST_JOURNAL_PARTIAL_BOUNDARY"); marker != "" {
		ctx = conversionPauseContext{Context: ctx, directory: c.StateDir, marker: marker}
	}
	if _, err := ConvertJournal(ctx, c); err != nil {
		t.Fatal(err)
	}
	// Remain observable until the parent kills the process. This permits either
	// committed format at the interruption boundary; it does not simulate a clock.
	time.Sleep(30 * time.Second)
}

func TestJournalConversionKilledDuringEncryptedBackup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix signal termination; native Windows qualification remains separate")
	}
	c, err := testConfig(t).validate()
	if err != nil {
		t.Fatal(err)
	}
	j, err := openJournalFormat(c, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.reserve(assignment("retained-action", "recovery")); err != nil {
		t.Fatal(err)
	}
	beforeRecords, err := j.records()
	if err != nil {
		t.Fatal(err)
	}
	original := j.path
	if err := j.close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(filepath.Dir(c.StateDir), "conversion-paused")
	child := exec.Command(os.Args[0], "-test.run=^TestJournalConversionProcess$", "-test.timeout=20s")
	child.Env = append(os.Environ(), "CPRA_TEST_JOURNAL_CONVERSION=1", "CPRA_TEST_JOURNAL_STATE="+c.StateDir, "CPRA_TEST_JOURNAL_KEY="+c.WrappingKeyPath, "CPRA_TEST_JOURNAL_PARTIAL_BOUNDARY="+marker)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	limit := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(limit) {
			t.Fatal("encrypted backup did not reach the interruption boundary")
		}
		time.Sleep(time.Millisecond)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	var killed *exec.ExitError
	if err := child.Wait(); !errors.As(err, &killed) || killed.ExitCode() != -1 {
		t.Fatal("conversion process was not killed", err)
	}
	partials, err := filepath.Glob(filepath.Join(c.StateDir, "worker.format2-*.db.partial-*"))
	if err != nil || len(partials) != 1 {
		t.Fatal("interruption did not leave exactly one partial backup", err)
	}
	partial, err := os.ReadFile(partials[0])
	if err != nil || len(partial) == 0 || len(partial) >= len(before) {
		t.Fatal("fixture did not interrupt a partial copy", err)
	}
	for range 3 {
		if _, err := ConvertJournal(t.Context(), c); !errors.Is(err, ErrConversionIncomplete) {
			t.Fatal("interrupted partial was not reported", err)
		}
		after, err := os.ReadFile(original)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("retry modified original encrypted journal", err)
		}
		retained, err := os.ReadFile(partials[0])
		if err != nil || !bytes.Equal(partial, retained) {
			t.Fatal("retry replaced partial backup evidence", err)
		}
		entries, err := os.ReadDir(c.StateDir)
		if err != nil || len(entries) != 2 {
			t.Fatal("retry created additional backup files", err)
		}
	}
	reopened, err := openJournalFormat(c, 2, true)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.close()
	afterRecords, err := reopened.records()
	if err != nil || !reflect.DeepEqual(beforeRecords, afterRecords) {
		t.Fatal("interrupted conversion changed protected execution records", err)
	}
}

func TestJournalConversionKilledAfterBackupPublication(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this signal-termination schedule is Unix-only; native Windows termination qualification is separate")
	}
	c, err := testConfig(t).validate()
	if err != nil {
		t.Fatal(err)
	}
	j, err := openJournalFormat(c, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 96; i++ {
		id := "protected-" + strconv.Itoa(i)
		if err = j.reserve(assignment(id, "recovery")); err != nil {
			t.Fatal(err)
		}
		if err = j.update(id, func(r *journalRecord) error {
			r.State, r.GrantID = "outcome", "grant"
			out := unknownOutcome("recovery", "")
			out.ServerID, out.WorkerUID, out.ExecutionID, out.GrantID = c.ServerID, c.WorkerUID, id, r.GrantID
			out.Data = json.RawMessage(`{"evidence":"` + strings.Repeat("x", 32<<10) + `"}`)
			r.Outcome = &out
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	before, err := j.records()
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(c.StateDir, "worker.format2-"+j.meta.ID+".db")
	if err = j.close(); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestJournalConversionProcess$", "-test.timeout=20s")
	child.Env = append(os.Environ(), "CPRA_TEST_JOURNAL_CONVERSION=1", "CPRA_TEST_JOURNAL_STATE="+c.StateDir, "CPRA_TEST_JOURNAL_KEY="+c.WrappingKeyPath)
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	limit := time.Now().Add(10 * time.Second)
	for {
		if _, err = os.Stat(backup); err == nil {
			break
		}
		if time.Now().After(limit) {
			t.Fatal("backup was not published")
		}
		time.Sleep(time.Millisecond)
	}
	if err = child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	var killed *exec.ExitError
	if err = child.Wait(); !errors.As(err, &killed) || killed.ExitCode() != -1 {
		t.Fatal("child did not terminate through the requested process kill", err)
	}
	restored, err := openJournalFormat(c, 2, true)
	format := 2
	if errors.Is(err, ErrJournalVersion) {
		format = 3
		restored, err = openJournalFormat(c, 3, true)
	}
	if err != nil {
		t.Fatal("interrupted conversion is unreadable", err)
	}
	defer restored.close()
	after, err := restored.records()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("interruption changed retained executions", err)
	}
	t.Logf("process killed after backup publication; recovered complete journal format %d", format)
}
