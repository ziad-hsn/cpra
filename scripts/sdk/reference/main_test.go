package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSource(t *testing.T, root, path, source string) {
	t.Helper()
	target := filepath.Join(root, "sdk/go", path)
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPublicReferenceExcludesPrivateReceiversAndFields(t *testing.T) {
	root := t.TempDir()
	writeSource(t, root, "client.go", `package cpra
type Public struct { Visible string; secret string }
type private struct{}
func (p *Public) Run() {}
func (p *private) Do() {}
func hidden() {}
`)
	entries, err := collect(root)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if strings.Contains(e.Name, "private") || e.Name == "hidden" || strings.Contains(e.Signature, "secret string") {
			t.Fatalf("private declaration escaped: %+v", e)
		}
		if e.Name == "*Public.Run" {
			found = true
			if strings.Contains(e.Signature, "{}") {
				t.Fatal("method body escaped")
			}
		}
	}
	if !found {
		t.Fatal("exported method omitted")
	}
}

func TestCombinedAPIHasAccuratePerSymbolBuildRequirements(t *testing.T) {
	root := t.TempDir()
	writeSource(t, root, "api/base.go", "//go:build !externaljobs\n\npackage api\ntype Monitor struct{}\nfunc Schema() []byte { return nil }\n")
	writeSource(t, root, "api/external.go", "//go:build externaljobs\n\npackage api\ntype Monitor struct{}\ntype Assignment struct{}\nfunc Schema() []byte { return nil }\n")
	writeSource(t, root, "worker/runner.go", "//go:build externaljobs\n\npackage worker\ntype Runner struct{}\n")
	entries, err := collect(root)
	if err != nil {
		t.Fatal(err)
	}
	tags := map[string]string{}
	for _, e := range entries {
		key := e.Package + "/" + e.Name
		if _, exists := tags[key]; exists {
			t.Fatalf("duplicate symbol %s", key)
		}
		tags[key] = e.Tag
	}
	for key, want := range map[string]string{"api/Monitor": "", "api/Schema": "", "api/Assignment": "externaljobs", "worker/Runner": "externaljobs"} {
		got, exists := tags[key]
		if !exists || got != want {
			t.Fatalf("%s tag %q exists %v; want %q", key, got, exists, want)
		}
	}
}
