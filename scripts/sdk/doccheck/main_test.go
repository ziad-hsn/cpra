package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOverviewUsesSelectedTaggedFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("doc.go", "//go:build externaljobs\n\n// Package worker runs optional handlers.\npackage worker\n")
	write("excluded.go", "// Package wrong must not appear.\npackage wrong\n")
	selected := selection{Dir: dir, ImportPath: "example.com/worker", Name: "worker", GoFiles: []string{"doc.go"}}
	got, err := overview(selected)
	if err != nil || !strings.HasPrefix(got, "Package worker runs optional handlers.") {
		t.Fatalf("rendered=%q error=%v", got, err)
	}
	write("duplicate.go", "// Package worker has a duplicate overview.\npackage worker\n")
	selected.GoFiles = append(selected.GoFiles, "duplicate.go")
	if _, err = overview(selected); err == nil || !strings.Contains(err.Error(), "got 2") {
		t.Fatalf("duplicate comment error=%v", err)
	}
}

func TestMissingOrMismatchedOverviewFails(t *testing.T) {
	for _, source := range []string{"package api\n", "// Package client uses the wrong name.\npackage api\n"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "doc.go"), []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := overview(selection{Dir: dir, ImportPath: "example.com/api", Name: "api", GoFiles: []string{"doc.go"}}); err == nil {
			t.Fatal("invalid overview was accepted")
		}
	}
}
