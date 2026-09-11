package durable

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDurableMetadataInitialAndReplacement(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state with spaces-é")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "identity.json")
	for i := 1; i <= 20; i++ {
		if err := atomicJSON(path, map[string]int{"generation": i}); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]int
		if err := json.Unmarshal(data, &got); err != nil || got["generation"] != i {
			t.Fatalf("replacement %d: %s, %v", i, data, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary metadata files leaked: %v, %v", entries, err)
	}
}

func TestDurableMetadataReplacementFailureRetainsDestination(t *testing.T) {
	dir := t.TempDir()
	destination := filepath.Join(dir, "occupied")
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(destination, map[string]int{"generation": 1}); err == nil {
		t.Fatal("replacing a directory must fail")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || !entries[0].IsDir() {
		t.Fatalf("failed replacement damaged destination or leaked temp: %v, %v", entries, err)
	}
}
