//go:build externaljobs

package worker

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestJournalConversionPartialRefusesAdditionalCopy(t *testing.T) {
	c, err := testConfig(t).validate()
	if err != nil {
		t.Fatal(err)
	}
	j, err := openJournalFormat(c, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	partial := filepath.Join(c.StateDir, "worker.format2-"+j.meta.ID+".db.partial-"+randomID())
	original := filepath.Join(c.StateDir, "worker.db")
	if err = j.close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	truncated := []byte("encrypted-partial-fixture")
	writePrivateTestFile(t, partial, truncated)
	for range 3 {
		if _, err = ConvertJournal(t.Context(), c); !errors.Is(err, ErrConversionIncomplete) {
			t.Fatal("partial was ignored", err)
		}
		after, err := os.ReadFile(original)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("original changed", err)
		}
		raw, err := os.ReadFile(partial)
		if err != nil || !bytes.Equal(raw, truncated) {
			t.Fatal("partial changed", err)
		}
		entries, err := os.ReadDir(c.StateDir)
		if err != nil || len(entries) != 2 {
			t.Fatal("retry allocated another backup", err, len(entries))
		}
	}
	reopened, err := openJournalFormat(c, 2, true)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.close()
}
